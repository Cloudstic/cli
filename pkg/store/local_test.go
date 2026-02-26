package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStore(t *testing.T) {
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "cloudstic-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}

	key := "test/key"
	data := []byte("test data")

	// Test Put
	if err := s.Put(key, data); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Test Exists
	exists, err := s.Exists(key)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("Key should exist")
	}

	// Test Get
	fetched, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(fetched) != string(data) {
		t.Errorf("Get returned wrong data: got %s, want %s", fetched, data)
	}

	// Test Exists - false
	exists, err = s.Exists("nonexistent")
	if err != nil {
		t.Fatalf("Exists(nonexistent) failed: %v", err)
	}
	if exists {
		t.Error("Nonexistent key should not exist")
	}

	// Test Delete
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	exists, err = s.Exists(key)
	if err != nil {
		t.Fatalf("Exists after delete failed: %v", err)
	}
	if exists {
		t.Error("Key should not exist after delete")
	}

	// Check nested structure
	key2 := "nested/dir/structure/key"
	if err := s.Put(key2, data); err != nil {
		t.Fatalf("Nested put failed: %v", err)
	}

	// Verify file exists on disk
	expectedPath := filepath.Join(tmpDir, "nested/dir/structure/key")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Error("File not found on disk at expected path")
	}
}
