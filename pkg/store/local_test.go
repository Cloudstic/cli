package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStore(t *testing.T) {
	ctx := context.Background()
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "cloudstic-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	s, err := NewLocalStore(tmpDir)
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
