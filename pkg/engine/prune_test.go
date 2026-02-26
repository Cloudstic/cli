package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/hamt"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/ui"
)

func TestPruneManager_Run(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()

	// 1. Setup Valid Chain
	chunkRef := "chunk/valid"
	_ = mockStore.Put(ctx, chunkRef, []byte("data"))

	content := core.Content{Chunks: []string{chunkRef}}
	fileContentHash := "valid-content-hash"
	contentRef := "content/" + fileContentHash

	_, contentData, _ := core.ComputeJSONHash(&content)
	_ = mockStore.Put(ctx, contentRef, contentData)

	meta := core.FileMeta{ContentHash: fileContentHash, Name: "valid.txt"}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef := "filemeta/" + metaHash
	_ = mockStore.Put(ctx, metaRef, metaData)

	// HAMT Construction using BackupManager's tree for flushing.
	src := NewMockSource()
	bkMgr := NewBackupManager(src, mockStore, ui.NewNoOpReporter(), WithVerbose())
	rootRef, err := bkMgr.tree.Insert("", "file1", metaRef)
	if err != nil {
		t.Fatalf("Failed to create hamt: %v", err)
	}
	if err := bkMgr.cache.Flush(rootRef); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Also insert directly into mock store (non-transactional).
	directTree := hamt.NewTree(mockStore)
	rootRef, err = directTree.Insert("", "file1", metaRef)
	if err != nil {
		t.Fatalf("Failed to insert: %v", err)
	}

	// Snapshot
	snap := core.Snapshot{Root: rootRef, Seq: 1}
	snapHash, snapData, _ := core.ComputeJSONHash(&snap)
	snapRef := "snapshot/" + snapHash
	_ = mockStore.Put(ctx, snapRef, snapData)

	// Index
	idx := core.Index{LatestSnapshot: snapRef, Seq: 1}
	idxData, _ := json.Marshal(idx)
	_ = mockStore.Put(ctx, "index/latest", idxData)

	// 2. Create Garbage (unreachable objects not referenced by any snapshot)
	_ = mockStore.Put(ctx, "chunk/garbage", []byte("trash"))
	_ = mockStore.Put(ctx, "filemeta/garbage", []byte("{}"))
	_ = mockStore.Put(ctx, "content/garbage", []byte("{}"))

	// 3. Run Prune
	metered := store.NewMeteredStore(mockStore)
	pm := NewPruneManager(metered, ui.NewNoOpReporter())
	result, err := pm.Run(ctx)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if result.ObjectsDeleted != 3 {
		t.Errorf("Expected 3 deleted, got %d", result.ObjectsDeleted)
	}

	// 4. Verify reachable objects remain
	assertExists(t, ctx, mockStore, chunkRef)
	assertExists(t, ctx, mockStore, contentRef)
	assertExists(t, ctx, mockStore, metaRef)
	assertExists(t, ctx, mockStore, rootRef)
	assertExists(t, ctx, mockStore, snapRef)
	assertExists(t, ctx, mockStore, "index/latest")

	// Verify garbage was swept
	assertNotExists(t, ctx, mockStore, "chunk/garbage")
	assertNotExists(t, ctx, mockStore, "filemeta/garbage")
	assertNotExists(t, ctx, mockStore, "content/garbage")
}

func assertExists(t *testing.T, ctx context.Context, s *MockStore, key string) {
	if exists, _ := s.Exists(ctx, key); !exists {
		t.Errorf("Expected %s to exist", key)
	}
}

func assertNotExists(t *testing.T, ctx context.Context, s *MockStore, key string) {
	if exists, _ := s.Exists(ctx, key); exists {
		t.Errorf("Expected %s to be deleted", key)
	}
}
