package engine

import (
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/pkg/store"
)

// packIndexCompactThreshold is how many stored pack-index objects a repository
// may accumulate before a backup consolidates them.
//
// The pack index is append-only: every flush writes its own shard, and every
// operation that opens the repository afterwards reads all of them, one request
// each. That cost grows with the number of backups a repository has ever taken
// and with nothing else — measured at 80 backups, `check`, `ls` and `find` each
// spent 81 requests on the index before doing any work of their own, and
// `backup` 86. Only `prune` bounded it, and a repository that is only ever
// backed up never runs one.
//
// Consolidating costs a write plus a delete per object replaced, so the
// threshold is what amortises it: crossing it every N backups spends about
// 1 + 1/N requests per backup to hold the read cost at a constant instead of a
// count of every flush in the repository's history.
const packIndexCompactThreshold = 16

// findPackStore walks down a store chain and returns its PackStore, or nil when
// packfiles are disabled.
//
// The chain is walked rather than the layer being held, because packing is
// optional: a caller that needs to compact or repack has to ask whether this
// repository packs at all, and the honest answer is whatever the chain it was
// handed contains.
func findPackStore(s store.ObjectStore) *storelayer.PackStore {
	for s != nil {
		if ps, ok := s.(*storelayer.PackStore); ok {
			return ps
		}
		un, ok := s.(store.Unwrapper)
		if !ok {
			return nil
		}
		s = un.Unwrap()
	}
	return nil
}
