package storelayer

import "sync"

// packRequestBytes is what one request is worth, in bytes, when deciding
// whether to transfer a whole pack or read its wanted objects individually.
//
// The decision is a comparison, not a threshold on object count: reading K
// objects totalling B bytes from a pack of size S costs either K requests and B
// bytes, or one request and S bytes. Whole is right when the K-1 requests it
// saves are worth the S-B bytes it adds, and that exchange rate is a property
// of the backend — round-trip latency against bandwidth and per-request price —
// rather than of the repository.
//
// One megabyte is where the curve knees on a replayed 82-pack restore trace:
// 1 MB gives 170 requests and 177.2 MB transferred, 256 KB gives 195 and
// 167.7 MB, 64 KB gives 497 and 139.4 MB. Nothing in that range wins on both
// axes, so this constant positions the trade rather than removing it. It is
// deliberately a constant for now; making it a per-backend value is worthwhile
// only once a backend has been measured to want a different one.
const packRequestBytes = 1 << 20

// packReadConcurrency is how many groups a caller may read at once.
//
// A worker reading a group holds that group's pack body until the group is
// done, so concurrency and residency are the same number here. The store's
// ConcurrencyHint is not that number: it answers how many requests should be in
// flight, and S3 asks for 128, which is right for uploading chunks and wrong
// for holding pack bodies. Running 128 workers against a cache that holds eight
// packs evicts each body before its group finishes and fetches it again --
// exactly the thrashing grouping exists to prevent.
//
// Derived rather than written down, and derived *here*, because both terms live
// in this package. Stating it in the engine meant hand-computing a storelayer
// constant in a package that cannot see it.
const packReadConcurrency = packBodyCacheBudget / maxPackSize

// packGroupPlan is what a caller's declared read set says about each pack it
// touches: how many objects are wanted, and how many bytes they come to.
//
// It exists because admission is otherwise a guess. Without it PackStore learns
// a pack's worth one miss at a time — ranged-reading up to packPromoteAfter
// objects to find out whether the pack was worth transferring whole, then
// inferring from a later eviction that it was not. A caller that has just handed
// over its whole read set has already answered the question exactly.
//
// It is bounded by the packs one grouping spans, not by the objects in them, and
// it is replaced wholesale by the next grouping rather than accumulating.
type packGroupPlan struct {
	mu    sync.Mutex
	count map[string]int   // wanted objects per pack, still unread
	bytes map[string]int64 // wanted bytes per pack
	size  map[string]int64 // the pack's object region, from the catalog
}

// consume records one of a pack's planned objects as read, dropping the pack
// once its plan is spent.
//
// Without this the plan outlives the reads it describes. A restore groups its
// metadata, works through it, and then reads file content in write order — and
// those reads would still find the metadata phase's counts and decide from
// them, transferring a whole pack because eighty of its objects were wanted by
// a pass that has already finished. Spent means spent: a later read of the same
// pack has nothing declared and falls back to the estimate, which is the honest
// answer for a read nobody planned.
func (p *packGroupPlan) consume(packRef string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n, ok := p.count[packRef]
	if !ok {
		return
	}
	if n <= 1 {
		delete(p.count, packRef)
		delete(p.bytes, packRef)
		delete(p.size, packRef)
		return
	}
	p.count[packRef] = n - 1
}

// fetchWhole reports whether the pack should be transferred as a unit, and
// whether the plan knows about it at all.
//
// A pack the plan does not name falls back to the miss-counting heuristic: an
// unplanned read — cat of one file, a dedup probe during backup — has nothing
// to state and nothing better to go on.
func (p *packGroupPlan) fetchWhole(packRef string) (whole, planned bool) {
	if p == nil {
		return false, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k, ok := p.count[packRef]
	if !ok {
		return false, false
	}
	region := p.size[packRef]
	if region <= 0 {
		// No catalog region for this pack. Transferring it whole risks an
		// unbounded read for an unknown gain, so prefer the bounded option.
		return false, true
	}
	excess := region - p.bytes[packRef]
	if excess <= 0 {
		return true, true
	}
	return int64(k-1)*packRequestBytes > excess, true
}
