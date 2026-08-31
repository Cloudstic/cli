package storelayer

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/pkg/store/local"
)

// packOneObject seals key into a packfile through its own PackStore and returns
// the backend holding it, so that later assertions run against a store whose
// catalog has never been loaded — the state a fresh process starts in.
func packOneObject(t *testing.T, ctx context.Context, key string, body []byte) *local.Store {
	t.Helper()

	backend, err := local.New(t.TempDir())
	if err != nil {
		t.Fatalf("create local store: %v", err)
	}
	writer, err := NewPackStore(backend)
	if err != nil {
		t.Fatalf("init writer pack store: %v", err)
	}
	if err := writer.Put(ctx, key, body); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// The object is genuinely packed: no standalone backend object by that
	// name, so nothing below can answer for it without the catalog.
	if standalone, err := backend.Exists(ctx, key); err != nil {
		t.Fatalf("backend exists %s: %v", key, err)
	} else if standalone {
		t.Fatalf("precondition failed: %s was stored standalone, so this test "+
			"would pass without the catalog being consulted", key)
	}
	return backend
}

// TestPackStore_SizeLoadsCatalogOnFirstCall is the Size half of the rule
// TestPackStore_ExistsLoadsCatalogOnFirstCall pins for Exists: a packed object
// has no standalone copy in the backend, so a store that has not loaded its
// catalog forwards the question down and gets the backend's "no such object".
//
// Unlike Exists, this one fails loudly rather than permissively, and it fails a
// mutation rather than a read: MeteredStore sizes an object before deleting it,
// so a Size that misses the catalog aborts the delete with a stat error before
// PackStore.Delete — which handles packed keys correctly — is ever reached.
// That is what made forget unable to remove a snapshot from any format-2
// repository whose snapshot object had been bundled.
//
// Size is called before anything else here on purpose. Every other read path
// loads the catalog, so any prior call would arm this one and hide the bug.
func TestPackStore_SizeLoadsCatalogOnFirstCall(t *testing.T) {
	ctx := context.Background()
	const key = "snapshot/deadbeef"
	body := []byte("packed snapshot")
	backend := packOneObject(t, ctx, key, body)

	// A fresh store, as a new process would have. Size is its first call.
	reader, err := NewPackStore(backend)
	if err != nil {
		t.Fatalf("init reader pack store: %v", err)
	}
	size, err := reader.Size(ctx, key)
	if err != nil {
		t.Fatalf("Size(%s) on a freshly constructed PackStore: %v", key, err)
	}
	if size != int64(len(body)) {
		t.Errorf("Size(%s) = %d, want %d", key, size, len(body))
	}
}

// TestPackStore_SizeUnpackedKeyNeedsNoCatalog is the counterpart of
// TestPackStore_ExistsUnpackedKeyNeedsNoCatalog: keys outside the packable
// namespaces can never be in the catalog, so sizing one must not pull the index
// in. MeteredStore sizes on every delete, which makes this the hot path.
func TestPackStore_SizeUnpackedKeyNeedsNoCatalog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	backend, err := local.New(dir)
	if err != nil {
		t.Fatalf("create local store: %v", err)
	}
	s, err := NewPackStore(backend)
	if err != nil {
		t.Fatalf("init pack store: %v", err)
	}
	body := []byte("snapshot/abc")
	if err := backend.Put(ctx, "index/latest", body); err != nil {
		t.Fatalf("put index/latest: %v", err)
	}

	size, err := s.Size(ctx, "index/latest")
	if err != nil {
		t.Fatalf("size index/latest: %v", err)
	}
	if size != int64(len(body)) {
		t.Errorf("Size(index/latest) = %d, want %d", size, len(body))
	}
	if s.catalogLoaded {
		t.Error("Size on an unpackable key loaded the pack catalog; nothing under " +
			"index/ is ever packed, so the load is pure cost")
	}
}

// TestMeteredOverPackStore_DeletesAPackedObject covers the two layers together,
// in the order the repository chain composes them. MeteredStore sizes before it
// deletes so it can credit the bytes back, and PackStore removes a packed key
// by rewriting its catalog rather than by touching the backend; the delete is
// only reachable if the size lookup resolves from the catalog too.
func TestMeteredOverPackStore_DeletesAPackedObject(t *testing.T) {
	ctx := context.Background()
	const key = "snapshot/deadbeef"
	backend := packOneObject(t, ctx, key, []byte("packed snapshot"))

	packed, err := NewPackStore(backend)
	if err != nil {
		t.Fatalf("init pack store: %v", err)
	}
	metered := NewMeteredStore(packed)

	if err := metered.Delete(ctx, key); err != nil {
		t.Fatalf("delete packed %s through the metered chain: %v", key, err)
	}
	if err := packed.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	exists, err := packed.Exists(ctx, key)
	if err != nil {
		t.Fatalf("exists %s after delete: %v", key, err)
	}
	if exists {
		t.Errorf("Exists(%s) = true after Delete; the catalog entry survived", key)
	}
}
