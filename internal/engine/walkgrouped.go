package engine

import (
	"context"

	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

// metaWalkBatch is how many entry refs a grouped walk buffers before reading
// them.
//
// The batch is what a streaming traversal trades for locality, so it should be
// the smallest number that gets the benefit. Replaying a real 82-pack restore
// trace, a window of 8,192 refs came within 3% of holding the entire plan
// (176 requests against 170), and the curve is flat from there — so this buys
// what an unbounded plan would, at a bounded cost. At roughly 70 bytes a ref
// that is well under a megabyte, against the pack bodies a read holds anyway.
const metaWalkBatch = 8192

// walkEntriesGrouped walks a snapshot's entries and hands each ref to fn, in
// batches ordered by where the objects physically live rather than in the order
// the walk produced them.
//
// A HAMT walk is a pass over hash buckets. It is deterministic, and on a
// repository built by one backup it happens to follow pack layout, because that
// backup wrote its objects during the same walk. On a repository built by
// eighty backups it does not: consecutive entries come from different packs, so
// a pack contributing entries to a traversal is contacted, dropped and
// contacted again.
//
// Handing the store a batch fixes both halves of that. The store orders the
// batch so a pack's entries are read consecutively, and it learns how much of
// each pack the batch wants — which is what lets it transfer a pack once
// instead of reading objects out of it one at a time to work out whether
// transferring it is worthwhile. Measured across the traversals at 82 packs,
// those probe reads are 77-88% of every command's requests.
//
// **fn is called in a different order than the walk produced.** Every caller
// here is order-independent by construction — check verifies each entry once,
// prune marks into a set, a listing builds a map — but a caller that needs walk
// order must not use this.
func walkEntriesGrouped(ctx context.Context, t *hamt.Tree, s store.ObjectStore, root string, fn func(valueRef string) error) error {
	buf := make([]string, 0, metaWalkBatch)

	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		for _, group := range store.PlanReads(ctx, s, buf).Groups {
			for _, ref := range group {
				if err := fn(ref); err != nil {
					return err
				}
			}
		}
		buf = buf[:0]
		return nil
	}

	if err := t.Walk(ctx, root, func(_, valueRef string) error {
		buf = append(buf, valueRef)
		if len(buf) < metaWalkBatch {
			return nil
		}
		return flush()
	}); err != nil {
		return err
	}
	return flush()
}
