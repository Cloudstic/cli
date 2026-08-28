package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
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

// prime stores an already-decoded filemeta under ref, so a later load costs no
// store read. The format-v3 paths use it: a leaf entry carries its meta bytes,
// and priming lets every ref-keyed consumer stay unchanged while never
// fetching a filemeta object — which, in a v3 repository, does not exist.
// A read-through loader (nil cache) drops the prime; v3 callers that may run
// after releaseCache must decode from the payload instead (decodePayloadMeta).
func (l *metaLoader) prime(ref string, fm core.FileMeta) {
	l.mu.Lock()
	if l.cache != nil {
		l.cache[ref] = fm
	}
	l.mu.Unlock()
}

// decodePayloadMeta decodes the filemeta a v3 leaf entry carries. ref names
// the bytes and is only used for the error message — the payload was verified
// as part of its leaf when the node was loaded.
func decodePayloadMeta(ref string, p *hamt.Payload) (*core.FileMeta, error) {
	var fm core.FileMeta
	if err := json.Unmarshal(p.Meta, &fm); err != nil {
		return nil, fmt.Errorf("decode leaf filemeta %s: %w", ref, err)
	}
	return &fm, nil
}

// loadMeta returns the filemeta for a tree entry, from its payload when the
// entry carries one (v3) and through the loader otherwise (v2). A payload hit
// also primes the loader, so ref-keyed re-reads during the same phase stay
// free either way.
func (l *metaLoader) loadMeta(ctx context.Context, ref string, p *hamt.Payload) (*core.FileMeta, error) {
	if p == nil {
		return l.load(ctx, ref)
	}
	fm, err := decodePayloadMeta(ref, p)
	if err != nil {
		return nil, err
	}
	l.prime(ref, *fm)
	return fm, nil
}

// cached reports whether ref has already been read through this loader. Backup
// uses it to skip re-queueing a filemeta it has already written this run.
func (l *metaLoader) cached(ref string) bool {
	l.mu.RLock()
	_, ok := l.cache[ref]
	l.mu.RUnlock()
	return ok
}

// releaseCache drops what the loader remembers and leaves it reading through,
// for a caller whose repeated reads are over.
//
// Memoizing is a property of a phase, not of a whole operation. Backup's scan
// reads the previous filemeta of every entry it visits and gets hits on the
// ones it revisits; what follows the scan — uploading file data — reads almost
// none of them and would carry a FileMeta per file through the longest and most
// allocation-heavy part of the run for nothing.
//
// Reading through afterwards is correct rather than merely tolerable: the cache
// only ever holds objects addressed by their own content, so a later read
// returns the same bytes it would have returned from memory.
func (l *metaLoader) releaseCache() {
	l.mu.Lock()
	l.cache = nil
	l.mu.Unlock()
}
