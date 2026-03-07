package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
)

// buildTestRepo sets up a minimal valid repository in the mock store and
// returns the snapshot ref and individual object keys for manipulation.
func buildTestRepo(t *testing.T, mockStore *MockStore) (snapRef, rootRef, metaRef, contentRef, chunkRef string) {
	t.Helper()
	ctx := context.Background()

	// Chunk
	chunkData := []byte("hello world")
	chunkHash := core.ComputeHash(chunkData)
	chunkRef = "chunk/" + chunkHash
	_ = mockStore.Put(ctx, chunkRef, chunkData)

	// Content
	content := core.Content{Chunks: []string{chunkRef}}
	contentHash, contentData, _ := core.ComputeJSONHash(&content)
	contentRef = "content/" + contentHash
	_ = mockStore.Put(ctx, contentRef, contentData)

	// FileMeta
	meta := core.FileMeta{
		Name:        "test.txt",
		Type:        core.FileTypeFile,
		ContentHash: contentHash,
		FileID:      "file1",
	}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef = "filemeta/" + metaHash
	_ = mockStore.Put(ctx, metaRef, metaData)

	// HAMT tree
	directTree := hamt.NewTree(mockStore)
	rootRef, err := directTree.Insert("", "file1", metaRef)
	if err != nil {
		t.Fatalf("Failed to build HAMT: %v", err)
	}

	// Snapshot
	snap := core.Snapshot{Root: rootRef, Seq: 1}
	snapHash, snapData, _ := core.ComputeJSONHash(&snap)
	snapRef = "snapshot/" + snapHash
	_ = mockStore.Put(ctx, snapRef, snapData)

	// Index
	idx := core.Index{LatestSnapshot: snapRef, Seq: 1}
	idxData, _ := json.Marshal(idx)
	_ = mockStore.Put(ctx, "index/latest", idxData)

	return snapRef, rootRef, metaRef, contentRef, chunkRef
}

func TestCheckManager_HealthyRepo(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	buildTestRepo(t, mockStore)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
	if result.SnapshotsChecked != 1 {
		t.Errorf("Expected 1 snapshot checked, got %d", result.SnapshotsChecked)
	}
	if result.ObjectsVerified == 0 {
		t.Error("Expected objects to be verified")
	}
}

func TestCheckManager_HealthyRepoWithReadData(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	buildTestRepo(t, mockStore)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheckManager_MissingChunk(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	_, _, _, _, chunkRef := buildTestRepo(t, mockStore)

	// Delete the chunk
	_ = mockStore.Delete(ctx, chunkRef)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
	if result.Errors[0].Key != chunkRef {
		t.Errorf("Expected error for %s, got %s", chunkRef, result.Errors[0].Key)
	}
	if result.Errors[0].Type != "missing" {
		t.Errorf("Expected error type 'missing', got %q", result.Errors[0].Type)
	}
}

func TestCheckManager_MissingContent(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	_, _, _, contentRef, _ := buildTestRepo(t, mockStore)

	_ = mockStore.Delete(ctx, contentRef)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
	if result.Errors[0].Key != contentRef {
		t.Errorf("Expected error for %s, got %s", contentRef, result.Errors[0].Key)
	}
	if result.Errors[0].Type != "missing" {
		t.Errorf("Expected error type 'missing', got %q", result.Errors[0].Type)
	}
}

func TestCheckManager_MissingFileMeta(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	_, _, metaRef, _, _ := buildTestRepo(t, mockStore)

	_ = mockStore.Delete(ctx, metaRef)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
	if result.Errors[0].Key != metaRef {
		t.Errorf("Expected error for %s, got %s", metaRef, result.Errors[0].Key)
	}
}

func TestCheckManager_MissingHAMTNode(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	_, rootRef, _, _, _ := buildTestRepo(t, mockStore)

	_ = mockStore.Delete(ctx, rootRef)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	// Deleting the root node causes HAMT walk errors which are reported
	if len(result.Errors) == 0 {
		t.Fatal("Expected at least 1 error for missing HAMT node")
	}
	// At least one error should reference the root node
	found := false
	for _, e := range result.Errors {
		if e.Key == rootRef {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected error referencing %s, got: %v", rootRef, result.Errors)
	}
}

func TestCheckManager_CorruptChunk_ReadData(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	_, _, _, _, chunkRef := buildTestRepo(t, mockStore)

	// Corrupt the chunk data
	_ = mockStore.Put(ctx, chunkRef, []byte("corrupted data"))

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
	if result.Errors[0].Key != chunkRef {
		t.Errorf("Expected error for %s, got %s", chunkRef, result.Errors[0].Key)
	}
	if result.Errors[0].Type != "corrupt" {
		t.Errorf("Expected error type 'corrupt', got %q", result.Errors[0].Type)
	}
}

func TestCheckManager_CorruptChunk_WithoutReadData(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	_, _, _, _, chunkRef := buildTestRepo(t, mockStore)

	// Corrupt the chunk data — should NOT be detected without --read-data
	_ = mockStore.Put(ctx, chunkRef, []byte("corrupted data"))

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Expected 0 errors without --read-data, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheckManager_SingleSnapshot(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	snapRef, _, _, _, _ := buildTestRepo(t, mockStore)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx, WithSnapshotRef(snapRef))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.SnapshotsChecked != 1 {
		t.Errorf("Expected 1 snapshot checked, got %d", result.SnapshotsChecked)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheckManager_SnapshotLatestAlias(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	buildTestRepo(t, mockStore)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), nil)
	result, err := cm.Run(ctx, WithSnapshotRef("latest"))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.SnapshotsChecked != 1 {
		t.Errorf("Expected 1 snapshot checked, got %d", result.SnapshotsChecked)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

// TestCheckManager_ContentRef_HMACPath verifies CheckManager follows meta.ContentRef (not ContentHash) to locate content objects.
func TestCheckManager_ContentRef_HMACPath(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	// Build a chunk
	chunkData := []byte("hello hmac world")
	chunkHash := core.ComputeHash(chunkData)
	chunkRef := "chunk/" + chunkHash
	_ = mockStore.Put(ctx, chunkRef, chunkData)

	// Build a content object stored under the HMAC ref
	contentHash := "plain-content-hash-abc"
	contentRef := crypto.ComputeHMAC(hmacKey, []byte(contentHash))
	content := core.Content{Chunks: []string{chunkRef}}
	_, contentData, _ := core.ComputeJSONHash(&content)
	_ = mockStore.Put(ctx, "content/"+contentRef, contentData)

	// FileMeta uses ContentHash + ContentRef (HMAC variant)
	meta := core.FileMeta{
		Name:        "hmac-file.txt",
		Type:        core.FileTypeFile,
		ContentHash: contentHash,
		ContentRef:  contentRef,
		FileID:      "hmac-file",
	}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef := "filemeta/" + metaHash
	_ = mockStore.Put(ctx, metaRef, metaData)

	// HAMT tree + snapshot
	directTree := hamt.NewTree(mockStore)
	rootRef, err := directTree.Insert("", "hmac-file", metaRef)
	if err != nil {
		t.Fatalf("Failed to build HAMT: %v", err)
	}
	snap := core.Snapshot{Root: rootRef, Seq: 1}
	snapHash, snapData, _ := core.ComputeJSONHash(&snap)
	snapRef := "snapshot/" + snapHash
	_ = mockStore.Put(ctx, snapRef, snapData)
	idx := core.Index{LatestSnapshot: snapRef, Seq: 1}
	idxData, _ := json.Marshal(idx)
	_ = mockStore.Put(ctx, "index/latest", idxData)

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), hmacKey)
	result, err := cm.Run(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

// TestCheckManager_CorruptChunk_HMACReadData verifies corruption detection with --read-data when chunks are HMAC-keyed.
func TestCheckManager_CorruptChunk_HMACReadData(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	// Build a clean chunk stored under HMAC key
	chunkData := []byte("clean chunk data")
	chunkHMAC := crypto.ComputeHMAC(hmacKey, chunkData)
	chunkRef := "chunk/" + chunkHMAC
	_ = mockStore.Put(ctx, chunkRef, chunkData)

	// Content object
	content := core.Content{Chunks: []string{chunkRef}}
	_, contentData, _ := core.ComputeJSONHash(&content)
	contentHash := "hmac-content-hash"
	contentRef := crypto.ComputeHMAC(hmacKey, []byte(contentHash))
	_ = mockStore.Put(ctx, "content/"+contentRef, contentData)

	// FileMeta
	meta := core.FileMeta{
		Name:        "corrupt-test.txt",
		Type:        core.FileTypeFile,
		ContentHash: contentHash,
		ContentRef:  contentRef,
		FileID:      "corrupt-hmac-file",
	}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef := "filemeta/" + metaHash
	_ = mockStore.Put(ctx, metaRef, metaData)

	directTree := hamt.NewTree(mockStore)
	rootRef, err := directTree.Insert("", "corrupt-hmac-file", metaRef)
	if err != nil {
		t.Fatalf("Failed to build HAMT: %v", err)
	}
	snap := core.Snapshot{Root: rootRef, Seq: 1}
	snapHash, snapData, _ := core.ComputeJSONHash(&snap)
	snapRef := "snapshot/" + snapHash
	_ = mockStore.Put(ctx, snapRef, snapData)
	idx := core.Index{LatestSnapshot: snapRef, Seq: 1}
	idxData, _ := json.Marshal(idx)
	_ = mockStore.Put(ctx, "index/latest", idxData)

	// Now corrupt the chunk
	_ = mockStore.Put(ctx, chunkRef, []byte("corrupted!"))

	cm := NewCheckManager(mockStore, ui.NewNoOpReporter(), hmacKey)
	result, err := cm.Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Expected 1 error for corrupt chunk, got %d: %v", len(result.Errors), result.Errors)
	}
	if result.Errors[0].Key != chunkRef {
		t.Errorf("Expected error for %s, got %s", chunkRef, result.Errors[0].Key)
	}
	if result.Errors[0].Type != "corrupt" {
		t.Errorf("Expected error type 'corrupt', got %q", result.Errors[0].Type)
	}
}
