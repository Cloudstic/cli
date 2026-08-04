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
// Eight packs' worth is the budget. It covers the working set of a repository in
// the size range the benchmark exercises while staying a stated ceiling rather
// than an emergent product, and a repository with smaller packs gets
// proportionally more of them — which a count-based cache could not express.
//
// This is deliberately larger than what it replaces. Going the other way was
// measured and is worse: at a 32 MB budget the same `check` peaks at 251 MB
// against 167 MB, because the transfers a smaller cache forces cost more than
// the residency it saves.
const packBodyCacheBudget = 8 * maxPackSize

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

// newPackBodyCache returns a cache holding at most budget bytes of pack bodies.
func newPackBodyCache(budget int) (*packBodyCache, error) {
	c := &packBodyCache{budget: budget}
	// The entry limit only has to be beyond what the budget can hold. A pack is
	// never smaller than one object, so this cannot be reached before the byte
	// budget is.
	inner, err := lru.NewWithEvict[string, []byte](1<<20, func(_ string, v []byte) {
		c.bytes -= len(v)
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
