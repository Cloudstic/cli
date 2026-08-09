package storelayer

import (
	"strings"
	"sync"
)

// packDemand tracks how many objects a caller has said it will still read from
// each pack, counted per key namespace.
//
// It is bounded by the packs a declared read set spans times the namespaces in
// them, not by the number of objects — the counts collapse a caller's whole plan
// into a handful of integers per pack. That is what keeps it from becoming the
// per-repository structure RFC 0023 exists to remove: nothing here grows with
// how much the repository holds, only with how far one operation's reads are
// spread.
//
// **Counts are per namespace because reads are.** A pack routinely mixes tree
// nodes, file metadata, chunk manifests and inlined file bodies, and an
// operation reads those in different passes. Counting them together lets a read
// of one namespace retire demand belonging to another: a `chunk/` read draining
// a pack's outstanding `content/` demand releases the body while later files
// still want it. That was measured before it was understood — release keyed on
// a single per-pack count cost 481 requests and 311.7 MB against 460 and
// 305.2 MB for not releasing at all.
type packDemand struct {
	mu sync.Mutex

	// Outstanding reads per (pack, namespace), and per pack across all
	// namespaces. The total is maintained alongside rather than summed on
	// demand because it is read on every served object.
	counts map[demandKey]int
	totals map[string]int

	// Live partial declarations per pack. A pack with one is not finished when
	// its counts reach zero, because more keys from it may still be declared:
	// restore's first pass names metadata objects and can only name the content
	// objects those point at once it has read them.
	//
	// Counted rather than flagged because declarations nest and are released
	// independently; a flag could not tell one outstanding partial declaration
	// from three.
	partial map[string]int
}

type demandKey struct {
	packRef string
	kind    string
}

// kindOf returns a key's namespace, including the separator: "content/abc" is
// "content/". Keys without one share the empty namespace, which is harmless —
// they are counted together and only ever compared with each other.
func kindOf(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i+1]
	}
	return ""
}

func newPackDemand() *packDemand {
	return &packDemand{
		counts:  make(map[demandKey]int),
		totals:  make(map[string]int),
		partial: make(map[string]int),
	}
}

// declare adds one declaration's counts.
func (d *packDemand) declare(counts map[demandKey]int, final bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	seenPack := make(map[string]struct{}, len(counts))
	for key, n := range counts {
		d.counts[key] += n
		d.totals[key.packRef] += n
		if final {
			continue
		}
		// One partial declaration per pack, however many namespaces it spans:
		// releasing it must decrement by exactly the same amount.
		if _, dup := seenPack[key.packRef]; !dup {
			seenPack[key.packRef] = struct{}{}
			d.partial[key.packRef]++
		}
	}
}

// release subtracts a declaration's counts, dropping entries that reach zero.
//
// It subtracts what was declared rather than clearing the pack outright, so a
// second declaration overlapping the first keeps its own outstanding demand.
// Counts already consumed by reads have been decremented, so the subtraction
// floors at zero rather than going negative.
func (d *packDemand) release(counts map[demandKey]int, final bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	seenPack := make(map[string]struct{}, len(counts))
	for key, n := range counts {
		if !final {
			if _, dup := seenPack[key.packRef]; !dup {
				seenPack[key.packRef] = struct{}{}
				if live := d.partial[key.packRef]; live <= 1 {
					delete(d.partial, key.packRef)
				} else {
					d.partial[key.packRef] = live - 1
				}
			}
		}

		remaining, ok := d.counts[key]
		if !ok {
			continue
		}
		drop := min(n, remaining)
		if remaining <= drop {
			delete(d.counts, key)
		} else {
			d.counts[key] = remaining - drop
		}
		if total := d.totals[key.packRef]; total <= drop {
			delete(d.totals, key.packRef)
		} else {
			d.totals[key.packRef] = total - drop
		}
	}
}

// outstanding reports how many declared objects remain unread in a pack, across
// every namespace.
func (d *packDemand) outstanding(packRef string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.totals[packRef]
}

// consume records one object of the given key served from a pack, and reports
// whether the pack is now finished — every namespace's declared demand
// exhausted, with no partial declaration that might still name more of it.
//
// A read of a namespace nothing declared decrements nothing. That is the point:
// it is how a chunk read stops retiring content demand it has nothing to do
// with.
//
// The bool distinguishes "finished" from "never declared", which a returned
// count of zero cannot: both would read as zero. Only the first means the body
// can be dropped.
func (d *packDemand) consume(packRef, key string) (finished bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	dk := demandKey{packRef: packRef, kind: kindOf(key)}
	remaining, ok := d.counts[dk]
	if !ok {
		return false
	}
	if remaining > 1 {
		d.counts[dk] = remaining - 1
	} else {
		delete(d.counts, dk)
	}

	total := d.totals[packRef]
	if total > 1 {
		d.totals[packRef] = total - 1
		return false
	}
	delete(d.totals, packRef)

	// Exhausted, but a live partial declaration means "this pass is done with
	// it", not "this operation is". Dropping the body here is what regressed
	// restore to 937 requests and 647.0 MB: the metadata pass released packs
	// the write pass then fetched again.
	return d.partial[packRef] == 0
}
