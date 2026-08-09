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
// Eight packs' worth is the budget. It stays a stated ceiling rather than an
// emergent product, and a repository with smaller packs gets proportionally
// more of them — which a count-based cache could not express.
//
// Going lower was measured and is worse: at a 32 MB budget `check` peaks at
// 251 MB against 167 MB for a working set that fits, because the transfers a
// smaller cache forces cost more than the residency it saves.
//
// Going *higher* to cover a bigger repository was measured too, and rejected:
// a budget that fits every pack of a 50,000-file repository (48 packs, 384 MB)
// makes residency track repository size, which is what RFC 0023 exists to
// remove — +347 MB peak RSS on `check`, +580 MB on `prune`. A number picked to
// fit one repository is not a bound.
//
// So this number is deliberately not sized to the largest repository anyone
// runs. What keeps exceeding it survivable is onPackEvicted: a pack whose
// promotion was evicted before paying for itself stops being promoted, so
// overrunning the cache degrades to ranged reads bounded by object size rather
// than repeated whole-pack transfers bounded by pack size (#458's
// amplification). That holds at any repository size, which a bigger constant
// does not.
//
// It is a floor, not a cure: ranged reads cost round trips, and a scan whose
// order moves between packs pays one per object. Fixing that means fixing the
// access order, the way #455 did for restore's write phase — see #478.
const packBodyCacheBudget = 8 * maxPackSize

// packBody is a cached packfile body together with the number of objects served
// from it since it was cached.
//
// The count lives with the body rather than in a structure of its own, and that
// is the entire point of it being here. They are one lifecycle: a body that is
// resident has a count, and a body that leaves takes its count with it. Held
// apart — which is how this worked until #494 — the two were bounded by
// different rules, so the count could be forgotten while the body was still
// resident, and the next eviction would read zero hits and penalize a pack that
// had been serving perfectly well.
type packBody struct {
	data []byte
	hits int
}

// packBodyCache is an LRU of packfile bodies bounded by total bytes.
//
// hashicorp/golang-lru bounds by entry count, so the byte accounting lives here:
// the underlying cache is given a generous entry limit and this type evicts from
// the tail until the budget is met.
type packBodyCache struct {
	mu     sync.Mutex
	lru    *lru.Cache[string, *packBody]
	bytes  int
	budget int

	// Called for a body dropped because the cache ran out of room, and only
	// for that, with the hits it served while cached. See newPackBodyCache.
	onPressureEvict func(key string, hits int)
}

// newPackBodyCache returns a cache holding at most budget bytes of pack bodies.
//
// onPressureEvict, if non-nil, is called when a body is dropped *because the
// cache ran out of room* — never when one is removed deliberately. PackStore
// reads that signal as "this promotion did not pay for itself" (see
// packAdmission.evicted), and a pack removed because it was deleted or its
// upload was discarded carries no such meaning: it is gone, not thrashing.
// Conflating the two marked deleted packs as not worth promoting and spent
// slots in a bounded window that exists to track live ones.
//
// The distinction is structural rather than a flag: the budget loop in Add is
// the only place a body is dropped under pressure, so it is the only place the
// signal is raised. The underlying LRU's own callback stays responsible for
// byte accounting, which every removal needs.
func newPackBodyCache(budget int, onPressureEvict func(key string, hits int)) (*packBodyCache, error) {
	c := &packBodyCache{budget: budget, onPressureEvict: onPressureEvict}
	// The entry limit only has to be beyond what the budget can hold. A pack is
	// never smaller than one object, so this cannot be reached before the byte
	// budget is.
	inner, err := lru.NewWithEvict(1<<20, func(_ string, v *packBody) {
		c.bytes -= len(v.data)
	})
	if err != nil {
		return nil, err
	}
	c.lru = inner
	return c, nil
}

// Get returns a cached body and counts the read against it.
//
// Counting here rather than in a separate call is what makes the tally
// inseparable from the body: every Get is a hit by definition, since the only
// caller reads an object out of what it returns.
func (c *packBodyCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	body, ok := c.lru.Get(key)
	if !ok {
		return nil, false
	}
	body.hits++
	return body.data, true
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
	c.lru.Add(key, &packBody{data: data})
	c.bytes += len(data)

	for c.bytes > c.budget && c.lru.Len() > 1 {
		evicted, body, ok := c.lru.RemoveOldest()
		if ok && c.onPressureEvict != nil {
			c.onPressureEvict(evicted, body.hits)
		}
	}
}

// Remove drops a body deliberately — the pack was deleted, or its upload was
// discarded. It raises no pressure signal: see newPackBodyCache.
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
