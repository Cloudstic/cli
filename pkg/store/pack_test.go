package store

import (
	"context"
	"os"
	"testing"
)

func TestPackStore_RepackOrphan(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cloudstic-pack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	localStore, err := NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}

	packStore, err := NewPackStore(localStore)
	if err != nil {
		t.Fatalf("Failed to init pack store: %v", err)
	}

	// Write some small files to trigger packing
	key1 := "filemeta/a"
	key2 := "filemeta/b"
	data1 := []byte("content A")
	data2 := []byte("content B")

	_ = packStore.Put(ctx, key1, data1)
	_ = packStore.Put(ctx, key2, data2)

	// Flush to ensure pack is created
	if err := packStore.Flush(ctx); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify pack exists
	packs, _ := localStore.List(ctx, packPrefix)
	if len(packs) != 1 {
		t.Fatalf("Expected 1 packfile, got %d", len(packs))
	}
	packRef := packs[0]

	packSize, _ := localStore.Size(ctx, packRef)

	// Now delete both keys logically
	_ = packStore.Delete(ctx, key1)
	_ = packStore.Delete(ctx, key2)

	// Flush to update catalog
	_ = packStore.Flush(ctx)

	// Trigger repack (orphan pack should be deleted)
	reclaimed, deletedPacks, err := packStore.Repack(ctx, 0.3)
	if err != nil {
		t.Fatalf("Repack failed: %v", err)
	}

	if deletedPacks != 1 {
		t.Errorf("Expected 1 pack to be deleted, got %d", deletedPacks)
	}
	if reclaimed != packSize {
		t.Errorf("Expected reclaimed bytes %d, got %d", packSize, reclaimed)
	}

	// Verify physically deleted
	exists, _ := localStore.Exists(ctx, packRef)
	if exists {
		t.Errorf("Orphaned packfile %s should have been physically deleted", packRef)
	}
}

func TestPackStore_RepackFragmented(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cloudstic-pack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	localStore, err := NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}

	packStore, err := NewPackStore(localStore)
	if err != nil {
		t.Fatalf("Failed to init pack store: %v", err)
	}

	// Write data where one file is large enough to trigger the repack threshold when deleted
	key1 := "filemeta/keep"
	key2 := "filemeta/delete"

	// Keep is small
	dataKeep := []byte("small")
	// Delete is larger to ensure waste > 30%
	dataDelete := make([]byte, 1024)
	for i := range dataDelete {
		dataDelete[i] = 'X'
	}

	_ = packStore.Put(ctx, key1, dataKeep)
	_ = packStore.Put(ctx, key2, dataDelete)
	_ = packStore.Flush(ctx)

	packs, _ := localStore.List(ctx, packPrefix)
	if len(packs) != 1 {
		t.Fatalf("Expected 1 packfile, got %d", len(packs))
	}
	originalPackRef := packs[0]

	// Logically delete the large part
	_ = packStore.Delete(ctx, key2)
	_ = packStore.Flush(ctx)

	// Repack (Threshold 0.3 means >30% empty. We deleted 1024 out of ~1029 bytes, so ~99% empty)
	reclaimed, deletedPacks, err := packStore.Repack(ctx, 0.3)
	if err != nil {
		t.Fatalf("Repack failed: %v", err)
	}

	if deletedPacks != 1 {
		t.Errorf("Expected 1 pack to be deleted during repack, got %d", deletedPacks)
	}
	// Reclaimed should be roughly the size of the deleted data
	if reclaimed < int64(len(dataDelete)) {
		t.Errorf("Expected reclaimed >= %d, got %d", len(dataDelete), reclaimed)
	}

	// Verify original is physically deleted
	exists, _ := localStore.Exists(ctx, originalPackRef)
	if exists {
		t.Errorf("Original fragmented packfile %s should have been physically deleted", originalPackRef)
	}

	// Verify a new pack was created
	newPacks, _ := localStore.List(ctx, packPrefix)
	if len(newPacks) != 1 {
		t.Fatalf("Expected exactly 1 new repacked packfile, got %d", len(newPacks))
	}
	if newPacks[0] == originalPackRef {
		t.Errorf("New packfile has same name as old one?")
	}

	// Verify the kept data is still accessible!
	fetched, err := packStore.Get(ctx, key1)
	if err != nil {
		t.Fatalf("Failed to get kept file after repack: %v", err)
	}
	if string(fetched) != string(dataKeep) {
		t.Errorf("Kept data mismatch: got %q, want %q", string(fetched), string(dataKeep))
	}
}

func TestPackStore_RepackNoFragment(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cloudstic-pack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	localStore, err := NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}

	packStore, err := NewPackStore(localStore)
	if err != nil {
		t.Fatalf("Failed to init pack store: %v", err)
	}

	key1 := "filemeta/keep1"
	key2 := "filemeta/keep2"

	_ = packStore.Put(ctx, key1, []byte("part 1"))
	_ = packStore.Put(ctx, key2, []byte("part 2"))
	_ = packStore.Flush(ctx)

	// Don't delete anything!

	reclaimed, deletedPacks, err := packStore.Repack(ctx, 0.3)
	if err != nil {
		t.Fatalf("Repack failed: %v", err)
	}

	if deletedPacks != 0 {
		t.Errorf("Expected 0 packs to be deleted, got %d", deletedPacks)
	}
	if reclaimed != 0 {
		t.Errorf("Expected 0 bytes reclaimed, got %d", reclaimed)
	}

	packs, _ := localStore.List(ctx, packPrefix)
	if len(packs) != 1 {
		t.Fatalf("Expected packfile to remain, got %d", len(packs))
	}
}
