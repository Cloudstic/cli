package engine

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
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

	assertExists(t, ctx, mockStore, chunkRef)
	assertExists(t, ctx, mockStore, contentRef)
	assertExists(t, ctx, mockStore, metaRef)
	assertExists(t, ctx, mockStore, rootRef)
	assertExists(t, ctx, mockStore, snapRef)
	assertExists(t, ctx, mockStore, "index/latest")

	assertNotExists(t, ctx, mockStore, "chunk/garbage")
	assertNotExists(t, ctx, mockStore, "filemeta/garbage")
	assertNotExists(t, ctx, mockStore, "content/garbage")
}

func TestPrune_ContentCacheHit(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()

	chunkRef := "chunk/c1"
	_ = mockStore.Put(ctx, chunkRef, []byte("data"))

	content := core.Content{Chunks: []string{chunkRef}}
	contentHash := "content-hash-1"
	contentRef := "content/" + contentHash
	_, contentData, _ := core.ComputeJSONHash(&content)
	_ = mockStore.Put(ctx, contentRef, contentData)

	meta := core.FileMeta{ContentHash: contentHash, Name: "file.txt"}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef := "filemeta/" + metaHash
	_ = mockStore.Put(ctx, metaRef, metaData)

	directTree := hamt.NewTree(mockStore)
	rootRef, _ := directTree.Insert("", "f1", metaRef)

	snap := core.Snapshot{Root: rootRef, Seq: 1}
	snapHash, snapData, _ := core.ComputeJSONHash(&snap)
	snapRef := "snapshot/" + snapHash
	_ = mockStore.Put(ctx, snapRef, snapData)
	idx := core.Index{LatestSnapshot: snapRef, Seq: 1}
	idxData, _ := json.Marshal(idx)
	_ = mockStore.Put(ctx, "index/latest", idxData)

	// Pre-populate content cache so prune doesn't need to GET content objects
	contentCache := ContentCatalog{contentRef: {chunkRef}}
	_ = SaveContentCache(mockStore, contentCache)

	// Add garbage
	_ = mockStore.Put(ctx, "chunk/orphan", []byte("orphan"))

	// Track GETs to verify no content object is fetched
	gets := &getTracker{MockStore: mockStore}
	metered := store.NewMeteredStore(gets)
	pm := NewPruneManager(metered, ui.NewNoOpReporter())
	result, err := pm.Run(ctx)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}

	if result.ObjectsDeleted != 1 {
		t.Errorf("expected 1 deleted (chunk/orphan), got %d", result.ObjectsDeleted)
	}
	assertNotExists(t, ctx, mockStore, "chunk/orphan")
	assertExists(t, ctx, mockStore, chunkRef)

	if gets.contentGets.Load() > 0 {
		t.Errorf("content cache should prevent content GETs, got %d", gets.contentGets.Load())
	}
}

func TestPrune_ContentCacheMissFallback(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()

	chunkRef := "chunk/c1"
	_ = mockStore.Put(ctx, chunkRef, []byte("data"))

	content := core.Content{Chunks: []string{chunkRef}}
	contentHash := "content-hash-1"
	contentRef := "content/" + contentHash
	_, contentData, _ := core.ComputeJSONHash(&content)
	_ = mockStore.Put(ctx, contentRef, contentData)

	meta := core.FileMeta{ContentHash: contentHash, Name: "file.txt"}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef := "filemeta/" + metaHash
	_ = mockStore.Put(ctx, metaRef, metaData)

	directTree := hamt.NewTree(mockStore)
	rootRef, _ := directTree.Insert("", "f1", metaRef)

	snap := core.Snapshot{Root: rootRef, Seq: 1}
	snapHash, snapData, _ := core.ComputeJSONHash(&snap)
	snapRef := "snapshot/" + snapHash
	_ = mockStore.Put(ctx, snapRef, snapData)
	idx := core.Index{LatestSnapshot: snapRef, Seq: 1}
	idxData, _ := json.Marshal(idx)
	_ = mockStore.Put(ctx, "index/latest", idxData)

	// No content cache → prune falls back to GET, chunks are still reachable
	_ = mockStore.Put(ctx, "chunk/orphan", []byte("orphan"))

	metered := store.NewMeteredStore(mockStore)
	pm := NewPruneManager(metered, ui.NewNoOpReporter())
	result, err := pm.Run(ctx)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}

	if result.ObjectsDeleted != 1 {
		t.Errorf("expected 1 deleted, got %d", result.ObjectsDeleted)
	}
	assertExists(t, ctx, mockStore, chunkRef)
	assertNotExists(t, ctx, mockStore, "chunk/orphan")

	// After fallback, cache should be saved with the fetched entry
	savedCache := LoadContentCache(mockStore)
	if _, ok := savedCache[contentRef]; !ok {
		t.Error("content cache should contain the fetched entry after miss")
	}
}

func TestPrune_ContentCacheRebuild(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()

	// Only live content
	content := core.Content{Chunks: []string{"chunk/live"}}
	contentHash := "live-hash"
	contentRef := "content/" + contentHash
	_, contentData, _ := core.ComputeJSONHash(&content)
	_ = mockStore.Put(ctx, contentRef, contentData)
	_ = mockStore.Put(ctx, "chunk/live", []byte("data"))

	meta := core.FileMeta{ContentHash: contentHash, Name: "file.txt"}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef := "filemeta/" + metaHash
	_ = mockStore.Put(ctx, metaRef, metaData)

	directTree := hamt.NewTree(mockStore)
	rootRef, _ := directTree.Insert("", "f1", metaRef)

	snap := core.Snapshot{Root: rootRef, Seq: 1}
	snapHash, snapData, _ := core.ComputeJSONHash(&snap)
	snapRef := "snapshot/" + snapHash
	_ = mockStore.Put(ctx, snapRef, snapData)
	idx := core.Index{LatestSnapshot: snapRef, Seq: 1}
	idxData, _ := json.Marshal(idx)
	_ = mockStore.Put(ctx, "index/latest", idxData)

	// Content cache with a stale entry + the live entry
	staleCache := ContentCatalog{
		contentRef:      {"chunk/live"},
		"content/stale": {"chunk/gone"},
	}
	_ = SaveContentCache(mockStore, staleCache)

	// Add unreachable objects so ObjectsDeleted > 0 (triggers rebuildCaches)
	_ = mockStore.Put(ctx, "content/stale", []byte("{}"))
	_ = mockStore.Put(ctx, "chunk/gone", []byte("orphan"))

	metered := store.NewMeteredStore(mockStore)
	pm := NewPruneManager(metered, ui.NewNoOpReporter())
	_, err := pm.Run(ctx)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}

	rebuilt := LoadContentCache(mockStore)
	if _, ok := rebuilt["content/stale"]; ok {
		t.Error("stale entry should have been pruned from content cache")
	}
	if _, ok := rebuilt[contentRef]; !ok {
		t.Error("live entry should remain in content cache")
	}
}

// getTracker wraps MockStore to count GET calls for content/ keys.
type getTracker struct {
	*MockStore
	contentGets atomic.Int64
}

func (g *getTracker) Get(ctx context.Context, key string) ([]byte, error) {
	if len(key) > 8 && key[:8] == "content/" {
		g.contentGets.Add(1)
	}
	return g.MockStore.Get(ctx, key)
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
