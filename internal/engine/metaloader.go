package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

// metaLoader reads and decodes the filemeta objects an operation walks over.
//
// Whether a loader memoizes is a property of the traversal, not of the call
// site. An operation that visits several snapshots — diff, prune, backup's
// change detection — meets the same ref repeatedly, because a file that did not
// change keeps its filemeta from one snapshot to the next. An operation that
// walks a single root meets every ref exactly once: a HAMT key derives from
// meta.FileID, which is itself a FileMeta field, so no two keys can share a
// filemeta ref. A cache there is pure overhead, which is what newUncached is
// for.
type metaLoader struct {
	store store.ObjectStore

	// mu guards cache, and is taken even by the single-threaded callers. An
	// uncontended RWMutex is a few nanoseconds against a store read, and paying
	// it unconditionally means no caller has to establish whether it is allowed
	// to share a loader before doing so.
	mu sync.RWMutex
	// cache is nil for a read-through loader, which is what distinguishes the
	// two kinds — a nil map reads as empty, so load simply never finds a hit.
	cache map[string]core.FileMeta
}

// newMetaLoader returns a loader that remembers every filemeta it reads.
func newMetaLoader(s store.ObjectStore) *metaLoader {
	return &metaLoader{store: s, cache: make(map[string]core.FileMeta)}
}

// newUncachedMetaLoader returns a loader that reads through on every call, for
// traversals that cannot get a hit or cannot afford to hold the whole tree.
func newUncachedMetaLoader(s store.ObjectStore) *metaLoader {
	return &metaLoader{store: s}
}

// load returns the filemeta stored at ref.
//
// The bytes are content-verified before decoding: ref names a SHA-256 of them,
// so a substituted or rotted object is refused here rather than decoded into a
// plausible-looking tree. See getVerified.
func (l *metaLoader) load(ctx context.Context, ref string) (*core.FileMeta, error) {
	l.mu.RLock()
	fm, ok := l.cache[ref]
	l.mu.RUnlock()
	if ok {
		return &fm, nil
	}

	data, err := getVerified(ctx, l.store, ref)
	if err != nil {
		return nil, fmt.Errorf("load filemeta %s: %w", ref, err)
	}
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil, fmt.Errorf("decode filemeta %s: %w", ref, err)
	}

	l.mu.Lock()
	if l.cache != nil {
		l.cache[ref] = fm
	}
	l.mu.Unlock()
	return &fm, nil
}

// cached reports whether ref has already been read through this loader. Backup
// uses it to skip re-queueing a filemeta it has already written this run.
func (l *metaLoader) cached(ref string) bool {
	l.mu.RLock()
	_, ok := l.cache[ref]
	l.mu.RUnlock()
	return ok
}
