package engine

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/cloudstic/cli/internal/hamt"
)

// restoreIOGap is how many bytes of unwanted data are worth transferring
// rather than paying a second round trip to skip over.
//
// This is the bandwidth-delay product — time-to-first-byte times bandwidth —
// and it is the rule Arrow coalesces file reads with: below it, reading and
// discarding the gap between two wanted ranges costs less than issuing a
// second request for the far side of it. It is also the quantity blobBudget is
// derived from, which is what makes the two agree by construction: a blob is
// sized at about one round trip's worth of bytes, so a restore that wants most
// of a blob collapses to a single whole-blob read without the planner needing
// to know anything about blobs.
//
// One megabyte is the conservative end of the band. RFC 0026 puts
// time-to-first-byte times bandwidth at 4.5–9 MB on a fast link and around
// 1 MB on a domestic uplink, and storelayer's packRequestBytes — the same
// exchange rate, arrived at by replaying a restore trace rather than derived —
// landed on 1 MB independently. Erring low is the right direction: it costs a
// full restore nothing, because a blob's members are contiguous and its gaps
// are the members nobody asked for, while it keeps a *partial* restore — a
// path filter, or a blob most of whose members have been forgotten — reading
// ranges instead of dragging whole blobs across the wire.
const restoreIOGap = 1 << 20

// restoreSpanBytes caps how far coalescing may grow one read.
//
// A span is fetched into a single buffer, so this is the largest allocation
// one request makes, and it is what stops a merge from amplifying: without it
// a blob whose wanted members are spread thinly across a large object would be
// transferred whole to serve a handful of bytes. It also bounds what a
// malformed or hostile repository can make a reader allocate, since Offset,
// Length and Total are values read off a store rather than computed here.
//
// Twice a blob's budget is slack rather than a limit: a blob seals at that
// much plaintext, so a span reaching this has already covered a whole one. A
// single member larger than it is still read — the cap governs merging, not
// the read a caller would have issued anyway.
//
// It follows blobLimit() rather than the blobBudget constant so that sweeping
// CLOUDSTIC_TEST_BLOB_BYTES sweeps the blob size and nothing else. A fixed cap
// would silently split every blob of a larger sweep into several reads, and
// report that as the larger blob's request cost.
func restoreSpanBytes() int64 { return 2 * blobLimit() }

// blobRead is one entry's body reference together with where that entry sits
// in the batch being planned. The index travels with the reference so a span
// can name the entries it serves without a second lookup.
type blobRead struct {
	index int
	ref   *hamt.BodyRef
}

// blobSpan is a contiguous run of one blob's bytes that a restore fetches as a
// unit, and the batch entries served out of it.
type blobSpan struct {
	blob   string
	offset int64
	length int64
	// members are the batch indices whose bodies lie inside this span, in
	// ascending offset order.
	members []int
}

// planBlobSpans turns a batch's body references into the reads a restore
// should actually issue.
//
// References are grouped by blob, ordered by offset, and merged while the gap
// between one and the next is under maxGap — the bandwidth-delay rule — and
// the result stays under maxSpan. Two entries sharing a member (the blob
// writer stores one body per content hash, so this is ordinary rather than
// exotic) produce overlapping references, whose gap is negative and which
// therefore merge into one span serving both.
//
// The output is deterministic: blobs come out in the order they were first
// seen in the batch, which is walk order, and each blob's spans in offset
// order.
func planBlobSpans(reads []blobRead, maxGap, maxSpan int64) []blobSpan {
	if len(reads) == 0 {
		return nil
	}

	byBlob := make(map[string][]blobRead)
	var order []string
	for _, r := range reads {
		if r.ref == nil || r.ref.Length <= 0 || r.ref.Offset < 0 {
			continue
		}
		if _, seen := byBlob[r.ref.Blob]; !seen {
			order = append(order, r.ref.Blob)
		}
		byBlob[r.ref.Blob] = append(byBlob[r.ref.Blob], r)
	}

	var spans []blobSpan
	for _, ref := range order {
		group := byBlob[ref]
		slices.SortFunc(group, func(a, b blobRead) int {
			if c := cmp.Compare(a.ref.Offset, b.ref.Offset); c != 0 {
				return c
			}
			return cmp.Compare(a.ref.Length, b.ref.Length)
		})

		cur := blobSpan{
			blob:    ref,
			offset:  group[0].ref.Offset,
			length:  group[0].ref.Length,
			members: []int{group[0].index},
		}
		for _, r := range group[1:] {
			end := cur.offset + cur.length
			// Written as a subtraction rather than a sum so the comparison
			// cannot depend on an overflowing offset+length.
			grown := max(end, r.ref.Offset+r.ref.Length) - cur.offset
			if r.ref.Offset-end <= maxGap && grown <= maxSpan {
				cur.length = grown
				cur.members = append(cur.members, r.index)
				continue
			}
			spans = append(spans, cur)
			cur = blobSpan{
				blob:    ref,
				offset:  r.ref.Offset,
				length:  r.ref.Length,
				members: []int{r.index},
			}
		}
		spans = append(spans, cur)
	}
	return spans
}

// slice returns one member's sealed bytes out of the span that was fetched for
// it. data must be exactly the span's bytes.
//
// The returned slice aliases data, which is the point: a span serves every
// member in it without copying. It is also why the span's buffer lives exactly
// as long as the last of these slices does, and so why a restore holds a span
// until the files cut out of it have been written.
func (s blobSpan) slice(data []byte, ref *hamt.BodyRef) ([]byte, error) {
	start := ref.Offset - s.offset
	// Bounds written as subtractions for the same reason the blob index
	// decoder does: both terms come off a store, and their sum can wrap.
	if start < 0 || ref.Length < 0 || start > int64(len(data)) || ref.Length > int64(len(data))-start {
		return nil, fmt.Errorf("member of %s at %d+%d lies outside the span read at %d+%d",
			s.blob, ref.Offset, ref.Length, s.offset, s.length)
	}
	return data[start : start+ref.Length], nil
}
