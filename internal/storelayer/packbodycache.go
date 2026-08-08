package storelayer

import (
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
)

// packBodyCacheBudget is how many bytes of packfile bodies PackStore keeps in
// memory.
//
// The cache held a fixed count of four packs, which was two problems. Its real
// size was that count times whatever a pack weighed — up to maxPackSize plus a
// footer each, a figure nobody stated. And four was below the working set of an
// ordinary repository, which is what made a miss expensive enough to matter: a
// miss re-reads an entire packfile to return one small object.
//
// Measured on a 50,000-file repository with two snapshots and 6 packs, `check`
// issues 127,789 lookups and misses 0.39% of them against a four-pack cache —
// 495 misses, each re-reading ~9 MB, which is the 4.2 GB of transfer that made
// `check` allocate 3 GB in the benchmark. At six packs the same trace misses six
// times. The access order is already good; the cache was simply smaller than
// what the order needs.
//
// Eight packs' worth was the first budget (RFC 0023, #460). It covered the six
// packs above with headroom, but the benchmark it was measured against grew:
// issue #467 added three incrementals, a 200 MB growth step and a deduplicated
// backup to every size, and a 50,000-file repository now produces 37 packs, not
// 6. Rebuilt directly and counted across the benchmark's range:
//
//	 5,000 files: 13 packs, 86 MB
//	20,000 files: 21 packs, 157 MB
//	50,000 files: 37 packs, 286 MB
//
// A live run against MinIO at the 5,000-file point showed the cost of staying
// at eight packs: `restore` transferred ~28 GB against a ~98 MB repository —
// about 280 times its size — the same whole-pack re-read #458 diagnosed,
// recurring at a scale the old budget never saw.
//
// Forty-eight packs' worth is the new budget: the same ~1.3x headroom over the
// top of the benchmark's range that eight packs was over six, so a repository
// built by any size the benchmark exercises today fits with room before this
// needs raising again. See RFC 0023's 2026-08-08 addendum and #474.
//
// Raising it again is now a tuning question, not a correctness one. A
// repository whose working set outgrows whatever this number is will always
// exist -- the budget is a stated ceiling, not an emergent product, and
// picking a bigger one only moves where that repository starts. What no
// longer scales with the miss is the *cost* of missing: onPackEvicted marks a
// pack whose promotion was evicted before it paid for itself, and
// resolveFromPack stops promoting a marked pack, falling back to ranged reads
// bounded by object size instead of repeating a whole-pack transfer bounded by
// pack size. A repository past this budget degrades to more ranged reads, not
// to the re-read amplification #458 and this recalibration both diagnosed.
//
// Going the other way was measured and is worse: at a 32 MB budget `check`
// peaks at 251 MB against 167 MB for a working set that fits, because the
// transfers a smaller cache forces cost more than the residency it saves.
const packBodyCacheBudget = 48 * maxPackSize

// packBodyCache is an LRU of packfile bodies bounded by total bytes.
//
// hashicorp/golang-lru bounds by entry count, so the byte accounting lives here:
// the underlying cache is given a generous entry limit and this type evicts from
// the tail until the budget is met.
type packBodyCache struct {
	mu     sync.Mutex
	lru    *lru.Cache[string, []byte]
	bytes  int
	budget int
}

// newPackBodyCache returns a cache holding at most budget bytes of pack
// bodies. onEvict, if non-nil, runs after eviction bookkeeping with the
// evicted key — PackStore uses it to tell a promotion that earned back its
// cost from one that didn't, which is what lets it stop re-promoting a pack
// the cache is too small to hold. See PackStore.onPackEvicted.
func newPackBodyCache(budget int, onEvict func(key string)) (*packBodyCache, error) {
	c := &packBodyCache{budget: budget}
	// The entry limit only has to be beyond what the budget can hold. A pack is
	// never smaller than one object, so this cannot be reached before the byte
	// budget is.
	inner, err := lru.NewWithEvict[string, []byte](1<<20, func(key string, v []byte) {
		c.bytes -= len(v)
		if onEvict != nil {
			onEvict(key)
		}
	})
	if err != nil {
		return nil, err
	}
	c.lru = inner
	return c, nil
}

func (c *packBodyCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Get(key)
}

// Add stores data under key and evicts from the tail until the cache is within
// budget.
//
// A single body larger than the whole budget is still cached, alone. Refusing it
// would mean re-reading that pack for every object taken out of it, which is the
// cost this cache exists to avoid.
func (c *packBodyCache) Add(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, existing := c.lru.Peek(key); existing {
		// The eviction callback adjusts the count for the value being replaced.
		c.lru.Remove(key)
	}
	c.lru.Add(key, data)
	c.bytes += len(data)

	for c.bytes > c.budget && c.lru.Len() > 1 {
		c.lru.RemoveOldest()
	}
}

func (c *packBodyCache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.Remove(key)
}

// byteLen reports the bytes currently held, for tests that pin the bound.
func (c *packBodyCache) byteLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// len reports how many bodies are held, for tests.
func (c *packBodyCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}
