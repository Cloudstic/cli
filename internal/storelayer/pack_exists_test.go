package storelayer

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/pkg/store/local"
)

// TestPackStore_ExistsLoadsCatalogOnFirstCall pins the one thing a packed
// object's presence must not depend on: whether something else happened to read
// the catalog first.
//
// A packed object has no standalone copy in the backend, so a PackStore that
// has not loaded its catalog finds the key in neither the active buffer nor the
// empty catalog and forwards the question to the backend, which correctly
// reports that no such object is stored there. The composite answer — (false,
// nil) for an object the repository holds — is wrong in the permissive
// direction, and callers such as copy's putIfMissing treat it as "not there
// yet" and rewrite the object.
//
// Exists is called before anything else here on purpose. Every other read path
// loads the catalog, so any prior call would arm this one and hide the bug.
func TestPackStore_ExistsLoadsCatalogOnFirstCall(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	backend, err := local.New(dir)
	if err != nil {
		t.Fatalf("create local store: %v", err)
	}

	// Write and seal a pack through one store, so the backend holds the object
	// only inside a packfile plus the index describing it.
	writer, err := NewPackStore(backend)
	if err != nil {
		t.Fatalf("init writer pack store: %v", err)
	}
	const key = "filemeta/deadbeef"
	if err := writer.Put(ctx, key, []byte("packed metadata")); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// The object is genuinely packed: no standalone backend object by that name.
	if standalone, err := backend.Exists(ctx, key); err != nil {
		t.Fatalf("backend exists %s: %v", key, err)
	} else if standalone {
		t.Fatalf("precondition failed: %s was stored standalone, so this test "+
			"would pass without the catalog being consulted", key)
	}

	// A fresh store, as a new process would have. Exists is its first call.
	reader, err := NewPackStore(backend)
	if err != nil {
		t.Fatalf("init reader pack store: %v", err)
	}
	exists, err := reader.Exists(ctx, key)
	if err != nil {
		t.Fatalf("exists %s: %v", key, err)
	}
	if !exists {
		t.Errorf("Exists(%s) = false on a freshly constructed PackStore; the packed "+
			"object is reported absent because the catalog was never loaded", key)
	}
}

// TestPackStore_ExistsUnpackedKeyNeedsNoCatalog checks the other half of the
// rule: keys outside the packable namespaces can never be in the catalog, so
// answering for one must not pull the index in. index/latest is the case that
// matters — prune probes it, and it is rewritten on every backup.
func TestPackStore_ExistsUnpackedKeyNeedsNoCatalog(t *testing.T) {
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
	if err := backend.Put(ctx, "index/latest", []byte("snapshot/abc")); err != nil {
		t.Fatalf("put index/latest: %v", err)
	}

	exists, err := s.Exists(ctx, "index/latest")
	if err != nil {
		t.Fatalf("exists index/latest: %v", err)
	}
	if !exists {
		t.Error("Exists(index/latest) = false, want true")
	}
	if s.catalogLoaded {
		t.Error("Exists on an unpackable key loaded the pack catalog; nothing under " +
			"index/ is ever packed, so the load is pure cost")
	}
}
