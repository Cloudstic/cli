package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	// Setup temp dir
	tmpDir := t.TempDir()

	s, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}

	key := "test/key"
	data := []byte("test data")

	// Test Put
	if err := s.Put(ctx, key, data); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Test Exists
	exists, err := s.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("Key should exist")
	}

	// Test Get
	fetched, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(fetched) != string(data) {
		t.Errorf("Get returned wrong data: got %s, want %s", fetched, data)
	}

	// Test Exists - false
	exists, err = s.Exists(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Exists(nonexistent) failed: %v", err)
	}
	if exists {
		t.Error("Nonexistent key should not exist")
	}

	// Test Delete
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	exists, err = s.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists after delete failed: %v", err)
	}
	if exists {
		t.Error("Key should not exist after delete")
	}

	// Check nested structure
	key2 := "nested/dir/structure/key"
	if err := s.Put(ctx, key2, data); err != nil {
		t.Fatalf("Nested put failed: %v", err)
	}

	// Verify file exists on disk
	expectedPath := filepath.Join(tmpDir, "nested/dir/structure/key")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Error("File not found on disk at expected path")
	}

	// Test Size
	size, err := s.Size(ctx, key2)
	if err != nil {
		t.Fatalf("Size() failed: %v", err)
	}
	if size != int64(len(data)) {
		t.Errorf("Expected size %d, got %d", len(data), size)
	}

	// Test TotalSize
	totalSize, err := s.TotalSize(ctx)
	if err != nil {
		t.Fatalf("TotalSize() failed: %v", err)
	}
	if totalSize != int64(len(data)) { // Remember we deleted `key` earlier.
		t.Errorf("Expected total size %d, got %d", len(data), totalSize)
	}

	// Test List
	if err := s.Put(ctx, "nested/dir/other", []byte("other")); err != nil {
		t.Fatalf("Nested put failed: %v", err)
	}

	keys, err := s.List(ctx, "nested")
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("Expected 2 keys under 'nested', got %d", len(keys))
	}
}

func TestStore_GetNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	s, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	_, err = s.Get(context.Background(), "missing/key")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want errors.Is(err, store.ErrNotFound)", err)
	}
}

// The shared RangeGetter contract. local implements RangeGetter and was the
// one backend never held to it — which is how it came to be the only one that
// does not reject a length it is about to allocate.
func TestLocalStoreRangeGetterConformance(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	storetest.AssertRangeGetterConformance(t, st)
}

// The shared SizedLister contract: the walk stats every file, so the size
// comes with the key and prune's sweep needs no request per object.
func TestLocalStoreSizedListerConformance(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	storetest.AssertSizedListerConformance(t, st)
}
