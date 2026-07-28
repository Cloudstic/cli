package storelayer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// index/latest is a mutable pointer, not content-addressed. Packing it ties its
// freshness to catalog-flush ordering and strands stale copies inside packs.
func TestPackStore_DoesNotPackMutableIndexKeys(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)
	ps := newPackStoreT(t, inner)

	mutable := []string{"index/latest", "index/packs", "index/snapshots", "index/lock/abc"}
	for _, key := range mutable {
		if ps.isSmallObject(key, []byte("snapshot/aaa")) {
			t.Errorf("%s must not be packed", key)
		}
	}

	for _, key := range []string{"filemeta/a", "node/b", "snapshot/c", "chunk/d", "content/e"} {
		if !ps.isSmallObject(key, []byte("small")) {
			t.Errorf("%s should still be packed", key)
		}
	}

	// index/latest must land in the backend directly, addressable without the catalog.
	if err := ps.Put(ctx, "index/latest", []byte("snapshot/aaa")); err != nil {
		t.Fatalf("put index/latest: %v", err)
	}
	if exists, err := inner.Exists(ctx, "index/latest"); err != nil || !exists {
		t.Errorf("index/latest should exist in the backend directly (exists=%v, err=%v)", exists, err)
	}
}

// Repositories written before the fix have index/latest inside a pack; those
// reads must keep working through the catalog.
func TestPackStore_LegacyPackedIndexLatestStaysReadable(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)

	// Simulate the legacy layout by packing the key explicitly.
	legacy := newPackStoreT(t, inner)
	legacy.mu.Lock()
	offset := int64(legacy.packBuffer.Len())
	legacy.packBuffer.Write([]byte("snapshot/legacy"))
	legacy.packKeys["index/latest"] = PackEntry{Offset: offset, Length: int64(len("snapshot/legacy"))}
	legacy.mu.Unlock()
	if err := legacy.Flush(ctx); err != nil {
		t.Fatalf("legacy flush: %v", err)
	}

	fresh := newPackStoreT(t, inner)
	data, err := fresh.Get(ctx, "index/latest")
	if err != nil {
		t.Fatalf("legacy packed index/latest unreadable: %v", err)
	}
	if string(data) != "snapshot/legacy" {
		t.Errorf("got %q, want %q", data, "snapshot/legacy")
	}
}

// deleteBlockingStore fails deletes of packfiles, standing in for a process
// interrupted after the replacement pack was flushed but before cleanup.
type deleteBlockingStore struct {
	store.ObjectStore
	blocked bool
}

func (d *deleteBlockingStore) Delete(ctx context.Context, key string) error {
	if d.blocked && strings.HasPrefix(key, packPrefix) {
		return errors.New("interrupted")
	}
	return d.ObjectStore.Delete(ctx, key)
}

// A repack interrupted around the deletion of the original pack must leave the
// data readable. Before the fix the objects existed only in memory at that
// point, so the interruption lost them.
func TestPackStore_RepackInterruptedBeforeDeleteLosesNothing(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cloudstic-repack-durability-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	localStore, err := local.New(tmpDir)
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	blocking := &deleteBlockingStore{ObjectStore: localStore}

	// Build a pack that is mostly waste so it qualifies for repacking.
	ps := newPackStoreT(t, blocking)
	survivor := "filemeta/survivor"
	if err := ps.Put(ctx, survivor, []byte("keep me")); err != nil {
		t.Fatalf("put survivor: %v", err)
	}
	for _, key := range []string{"filemeta/waste1", "filemeta/waste2", "filemeta/waste3"} {
		if err := ps.Put(ctx, key, []byte(strings.Repeat("x", 400))); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	for _, key := range []string{"filemeta/waste1", "filemeta/waste2", "filemeta/waste3"} {
		if err := ps.Delete(ctx, key); err != nil {
			t.Fatalf("delete %s: %v", key, err)
		}
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatalf("flush after deletes: %v", err)
	}

	// Interrupt at the point where the old pack would be removed.
	blocking.blocked = true
	if _, _, err := ps.Repack(ctx, 0.3); err == nil {
		t.Fatal("expected the blocked delete to surface as an error")
	}
	blocking.blocked = false

	// A fresh session, reading only what reached storage, must still find it.
	recovered := newPackStoreT(t, localStore)
	data, err := recovered.Get(ctx, survivor)
	if err != nil {
		t.Fatalf("survivor lost after interrupted repack: %v", err)
	}
	if string(data) != "keep me" {
		t.Errorf("got %q, want %q", data, "keep me")
	}

	// The superseded pack is a recoverable orphan; a later repack reclaims it.
	if _, _, err := recovered.Repack(ctx, 0.3); err != nil {
		t.Fatalf("cleanup repack: %v", err)
	}
	if _, err := recovered.Get(ctx, survivor); err != nil {
		t.Errorf("survivor lost after cleanup repack: %v", err)
	}
}
