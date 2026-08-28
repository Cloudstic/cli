package engine

import (
	"context"

	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

// entryBatch is how many entries a traversal buffers before reading them.
//
// The batch is what a streaming traversal trades for locality, so it should be
// the smallest number that gets the benefit. Replaying a real 82-pack restore
// trace, a window of 8,192 refs came within 3% of holding the entire plan
// (176 requests against 170), and the curve is flat from there — so this buys
// what an unbounded plan would, at a bounded cost.
//
// It also bounds a traversal's memory. A pass that has to carry something per
// entry — check and prune both carry the content object each entry names —
// holds it for one batch rather than for the whole snapshot.
const entryBatch = 8192

// treeEntry is one HAMT entry: the key it is routed under, the ref of the
// filemeta it names, and — in a v3 repository — the payload its leaf carries.
//
// The key travels with the ref because some callers need it — find matches
// against it and records folders by it — and a batch that dropped it would
// force those callers to walk separately.
type treeEntry struct {
	key     string
	ref     string
	payload *hamt.Payload
}

// walkEntriesBatched walks a snapshot's entries and hands them to fn in
// fixed-size batches.
//
// It does batching and nothing else. What a caller does with a batch — one
// grouped read, or two for a traversal that reads a second namespace per entry
// — is the caller's business, because that shape differs and hiding it behind a
// mode flag would make every caller harder to read than the loop it replaced.
func walkEntriesBatched(ctx context.Context, t *hamt.Tree, root string, fn func([]treeEntry) error) error {
	buf := make([]treeEntry, 0, entryBatch)

	if err := t.WalkEntries(ctx, root, func(key, ref string, p *hamt.Payload) error {
		buf = append(buf, treeEntry{key: key, ref: ref, payload: p})
		if len(buf) < entryBatch {
			return nil
		}
		if err := fn(buf); err != nil {
			return err
		}
		buf = buf[:0]
		return nil
	}); err != nil {
		return err
	}
	if len(buf) == 0 {
		return nil
	}
	return fn(buf)
}

// splitByPayload partitions a batch into the entries whose meta rides in their
// leaf (v3) and the refs that must be fetched (v2). Callers process the first
// group directly and declare only the second to the store — a declaration
// containing keys nothing will read would misinform grouping.
func splitByPayload(entries []treeEntry) (carried []treeEntry, refs []string) {
	refs = make([]string, 0, len(entries))
	for _, e := range entries {
		if e.payload != nil {
			carried = append(carried, e)
		} else {
			refs = append(refs, e.ref)
		}
	}
	return carried, refs
}

// readGrouped declares keys to the store and invokes fn for each one, in the
// order the store nominates rather than the order given.
//
// A HAMT walk is a pass over hash buckets. On a repository built by one backup
// it happens to follow storage layout, because that backup wrote its objects
// during the same walk; on one built by eighty backups it does not, so a bundle
// contributing entries is contacted, dropped and contacted again. Declaring
// fixes both halves: the store orders the batch so a bundle's objects are read
// consecutively, and it learns how much of each bundle the batch wants, which
// is what lets it transfer one once instead of reading objects out of it one at
// a time to find out whether transferring it is worthwhile. Measured across the
// traversals at 82 packs, those probe reads were 77-88% of every command's
// requests.
//
// **fn is called in a different order than keys were given.** Every caller is
// order-independent by construction — check verifies each entry once, prune
// marks into a set, ls builds a map, find sorts its matches before returning —
// but a caller that needs the original order must not use this.
//
// A key repeated in keys is passed to fn once per occurrence: a deduplicated
// object read by several entries is several reads, and collapsing them would
// understate the demand.
func readGrouped(ctx context.Context, s store.ObjectStore, keys []string, fn func(key string) error) error {
	if len(keys) == 0 {
		return nil
	}
	for _, group := range store.PlanReads(ctx, s, keys).Groups {
		for _, key := range group {
			if err := fn(key); err != nil {
				return err
			}
		}
	}
	return nil
}
