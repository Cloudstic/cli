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
	mockStore := NewMockStore()

	// 1. Setup Valid Chain
	chunkRef := "chunk/valid"
	mockStore.Put(chunkRef, []byte("data"))

	content := core.Content{Chunks: []string{chunkRef}}
	fileContentHash := "valid-content-hash"
	contentRef := "content/" + fileContentHash

	_, contentData, _ := core.ComputeJSONHash(&content)
	mockStore.Put(contentRef, contentData)

	meta := core.FileMeta{ContentHash: fileContentHash, Name: "valid.txt"}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef := "filemeta/" + metaHash
	mockStore.Put(metaRef, metaData)

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
	mockStore.Put(snapRef, snapData)

	// Index
	idx := core.Index{LatestSnapshot: snapRef, Seq: 1}
	idxData, _ := json.Marshal(idx)
	mockStore.Put("index/latest", idxData)

	// 2. Create Garbage (unreachable objects not referenced by any snapshot)
	mockStore.Put("chunk/garbage", []byte("trash"))
	mockStore.Put("filemeta/garbage", []byte("{}"))
	mockStore.Put("content/garbage", []byte("{}"))

	// 3. Run Prune
	metered := store.NewMeteredStore(mockStore)
	pm := NewPruneManager(metered, ui.NewNoOpReporter())
	result, err := pm.Run(context.Background())
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if result.ObjectsDeleted != 3 {
		t.Errorf("Expected 3 deleted, got %d", result.ObjectsDeleted)
	}

	// 4. Verify reachable objects remain
	assertExists(t, mockStore, chunkRef)
	assertExists(t, mockStore, contentRef)
	assertExists(t, mockStore, metaRef)
	assertExists(t, mockStore, rootRef)
	assertExists(t, mockStore, snapRef)
	assertExists(t, mockStore, "index/latest")

	// Verify garbage was swept
	assertNotExists(t, mockStore, "chunk/garbage")
	assertNotExists(t, mockStore, "filemeta/garbage")
	assertNotExists(t, mockStore, "content/garbage")
}

func assertExists(t *testing.T, s *MockStore, key string) {
	if exists, _ := s.Exists(key); !exists {
		t.Errorf("Expected %s to exist", key)
	}
}

func assertNotExists(t *testing.T, s *MockStore, key string) {
	if exists, _ := s.Exists(key); exists {
		t.Errorf("Expected %s to be deleted", key)
	}
}
