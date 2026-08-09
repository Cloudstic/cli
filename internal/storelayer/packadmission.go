package storelayer

import (
	"fmt"

	lru "github.com/hashicorp/golang-lru/v2"
)

// packAdmission decides whether a pack is worth transferring whole, from
// nothing but the history of reads against it.
//
// It is an *estimator*, and that is the important thing about it. The quantity
// it approximates -- how many objects this operation will read from this pack
// -- is one a caller frequently knows outright: a restore holds its whole
// metadata plan before it fetches anything. Probing estimates that number
// forward, one miss at a time; the penalty estimates it backward from an
// eviction that already happened. Both exist because nothing tells the store
// the number.
//
// So this type is scaffolding with a demolition date. When callers state their
// demand (RFC 0025 §2) or compaction keeps working sets inside the body cache
// (RFC 0025 §4), the estimate stops earning its keep, and this type and the one
// field holding it are the whole of what has to be deleted. Keeping it in one
// place, behind one entry point, is what makes that a small change rather than
// surgery across four call sites -- which is what it was before this type
// existed.
//
// Concurrency, stated accurately because the obvious reading is wrong in both
// directions. The three caches are individually safe for concurrent use --
// hashicorp/golang-lru guards each with its own mutex -- and PackStore does
// *not* serialise these calls: Get invokes served and recordMiss after
// releasing its own lock, and the body cache invokes evicted from wherever the
// eviction happened.
//
// What is not atomic is the read-modify-write inside recordMiss and served: two
// concurrent misses on the same pack can both read n and both write n+1, losing
// one. That is a lost update on an estimate that is approximate by
// construction, costing at most a one-miss delay to a promotion, and it is the
// behaviour these fields already had. Making it atomic would change promotion
// timing under concurrency, which is a behavioural change and not what this
// type's extraction is.
type packAdmission struct {
	// Misses per pack since it was last cached, used to decide when reading one
	// object at a time is no longer the cheaper option.
	misses *lru.Cache[string, int]

	// Hits served from a pack since it was promoted, read by evicted() to tell
	// a promotion that paid for itself from one that didn't.
	hits *lru.Cache[string, int]

	// Packs whose last promotion was evicted before serving packPromoteAfter
	// hits -- cheaper to have left as ranged reads. A pack in here is never
	// promoted again until it ages out of this bounded structure, which is what
	// keeps a working set bigger than the body cache from paying a whole-pack
	// transfer over and over: the cost per object converges to a ranged read,
	// bounded by object size, rather than staying bounded by pack size no
	// matter how much the true working set overruns the cache.
	penalized *lru.Cache[string, struct{}]
}

const (
	// packPromoteAfter is how many times a pack may be read one object at a time
	// before the whole thing is fetched and cached instead.
	//
	// The two access patterns this sits between were measured (RFC 0023). A scan
	// -- check, ls, prune -- walks a pack's objects consecutively, so one transfer
	// serves thousands of reads and ranged reads would be thousands of round
	// trips instead of one. A scattered read -- restore's write phase, or a cat
	// of a single file -- touches a pack once or twice, so fetching up to
	// maxPackSize to return a few hundred bytes is nearly all waste.
	//
	// The threshold is asymmetric in what it costs each pattern, which is why it
	// is not 2. A scan pays at most packPromoteAfter-1 extra small requests per
	// pack visit -- a few hundred across a repository, against transfers it makes
	// anyway. A scattered read pays one whole pack per packPromoteAfter misses,
	// which is bytes, and bytes are what a remote backend bills for.
	//
	// Measured on a 51-pack tree, restore's whole-pack transfers against this
	// value: 2857 at 2, 1419 at 4, 719 at 8, 358 at 16, 179 at 32. Most of the
	// benefit is captured by 8, and beyond it the round trips traded away start
	// to matter more than the bytes saved.
	packPromoteAfter = 8

	// packMissWindow bounds the miss counter, the hit counter and the penalty
	// set. Each only has to span the packs in flight around a given moment, and
	// none may become a second unbounded per-repository structure -- which is
	// the thing RFC 0023 is about.
	//
	// It has to stay above the number of packs the body cache holds at once. A
	// hit counter evicted from this window while its pack is still resident
	// reads as zero hits on the next eviction and penalizes a pack that was
	// serving fine.
	//
	// 64 does not actually guarantee that, and the comment here used to claim it
	// did. The figure it was chosen against -- packBodyCacheBudget /
	// maxPackSize, or 8 -- assumes every pack is maximal, while packBodyCache
	// bounds bytes rather than entries, so a repository of small incremental
	// packs can hold far more than 64 resident. Latent rather than active at
	// measured scale: at 82 packs roughly 32 are resident, and raising this to
	// 512 there moved nothing. See #494 for the fix, which is behavioural.
	packMissWindow = 64
)

func newPackAdmission() (*packAdmission, error) {
	misses, err := lru.New[string, int](packMissWindow)
	if err != nil {
		return nil, fmt.Errorf("pack miss counter init: %w", err)
	}
	hits, err := lru.New[string, int](packMissWindow)
	if err != nil {
		return nil, fmt.Errorf("pack hit counter init: %w", err)
	}
	penalized, err := lru.New[string, struct{}](packMissWindow)
	if err != nil {
		return nil, fmt.Errorf("pack penalty tracker init: %w", err)
	}
	return &packAdmission{misses: misses, hits: hits, penalized: penalized}, nil
}

// recordMiss counts one miss against packRef and reports whether the pack has
// now missed often enough to be worth transferring whole, along with the miss
// count itself for logging.
//
// Counting and deciding are one call because they are one event: every miss
// both advances the estimate and consults it, and splitting them would let a
// caller consult without advancing, which is how a promotion threshold never
// fires.
func (a *packAdmission) recordMiss(packRef string) (misses int, fetchWhole bool) {
	if n, ok := a.misses.Get(packRef); ok {
		misses = n
	}
	misses++
	a.misses.Add(packRef, misses)

	// Peek, not Get: checking the penalty must not refresh its recency, or a
	// pack read every miss (which a penalized one is, by definition) would
	// keep itself permanently fresh in penalized and never age out for the
	// second chance the comment on that field promises.
	_, penalized := a.penalized.Peek(packRef)

	// Worth transferring whole once it has missed enough to look like a scan,
	// unless a previous promotion was evicted before paying for itself.
	return misses, misses >= packPromoteAfter && !penalized
}

// cached records that packRef is now held whole in the body cache, so the
// misses that led there are spent.
func (a *packAdmission) cached(packRef string) {
	a.misses.Remove(packRef)
}

// served records one object served from a cached pack, read back by evicted.
func (a *packAdmission) served(packRef string) {
	hits := 0
	if n, ok := a.hits.Get(packRef); ok {
		hits = n
	}
	a.hits.Add(packRef, hits+1)
}

// evicted records that a cached pack was dropped because the cache ran out of
// room -- not that one was removed deliberately, which carries no such meaning
// (see newPackBodyCache).
//
// A promotion pays for a whole-pack transfer up front, betting it back on the
// hits the pack serves while cached. That bet loses when the working set is
// bigger than the cache: the pack is evicted to make room for another before it
// has served enough hits to be worth what it cost, and the next miss on the
// same pack would promote it again, pay the same transfer, and lose the same
// bet -- forever, at a cost bounded by pack size no matter how much larger the
// true working set is than the cache.
//
// Recording the loss here is what breaks that cycle. A pack that didn't earn
// back its promotion is marked, and recordMiss serves it with ranged reads from
// then on rather than promoting it again -- bounding the cost per object by
// object size instead, which holds regardless of how large the repository or
// how mismatched the cache is to it. The mark expires when it ages out of the
// penalty set's own bounded window, so a pack that becomes worth caching again
// -- the working set shrinks, or access moves on -- gets another chance rather
// than being penalized permanently on one bad measurement.
func (a *packAdmission) evicted(packRef string) {
	hits := 0
	if n, ok := a.hits.Get(packRef); ok {
		hits = n
	}
	a.hits.Remove(packRef)
	if hits < packPromoteAfter {
		a.penalized.Add(packRef, struct{}{})
	}
}
