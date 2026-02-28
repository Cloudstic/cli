package store

import (
	"context"
	"os"
	"testing"
)

func TestCompressedStore(t *testing.T) {
	ctx := context.Background()

	// 1. Setup local Datastore
	tmpDir, err := os.MkdirTemp("", "cloudstic-compressed-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	localStore, err := NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}

	compressedStore := NewCompressedStore(localStore)

	// --- 2. Test lifecycle with highly compressible data ---
	key1 := "test/compressible.txt"
	compressibleData := make([]byte, 1024*10) // 10KB of zeros
	for i := range compressibleData {
		compressibleData[i] = 'A' // Not completely 0 so tests are fair
	}

	// Put
	if err := compressedStore.Put(ctx, key1, compressibleData); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Make sure compression actually happens by grabbing raw underlying size limit
	rawSize, err := localStore.Size(ctx, key1)
	if err != nil {
		t.Fatalf("Underlying Size failed: %v", err)
	}
	if rawSize >= int64(len(compressibleData)) {
		t.Errorf("Expected raw file to be compressed, got raw size %d >= original size %d", rawSize, len(compressibleData))
	}

	// Get
	fetched, err := compressedStore.Get(ctx, key1)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(fetched) != len(compressibleData) {
		t.Fatalf("Get mismatch length. want %d, got %d", len(compressibleData), len(fetched))
	}

	// Exists
	exists, err := compressedStore.Exists(ctx, key1)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Errorf("Expected exists to be true")
	}

	// --- 3. Test incompressible data fallback ---
	key2 := "test/incompressible.txt"
	incompressibleData := []byte("tiny")

	if err := compressedStore.Put(ctx, key2, incompressibleData); err != nil {
		t.Fatalf("Put incompressible failed: %v", err)
	}

	// GZIP overhead will make this *larger* than the original payload,
	// so it should fallback exactly transparently to raw bytes.
	rawSize2, err := localStore.Size(ctx, key2)
	if err != nil {
		t.Fatalf("Underlying Size failed: %v", err)
	}
	if rawSize2 != int64(len(incompressibleData)) {
		t.Errorf("Expected raw file to be unchanged length, got raw size %d vs original size %d", rawSize2, len(incompressibleData))
	}

	// Get
	fetched2, err := compressedStore.Get(ctx, key2)
	if err != nil {
		t.Fatalf("Get incompressible failed: %v", err)
	}
	if string(fetched2) != string(incompressibleData) {
		t.Fatalf("Get incompressible mismatch. want %q, got %q", string(incompressibleData), string(fetched2))
	}

	// --- 4. Store passthrough logic ---
	size, err := compressedStore.Size(ctx, key1)
	if err != nil {
		t.Fatalf("Size failed: %v", err)
	}
	if size != rawSize {
		t.Errorf("Store size %d doesn't match raw underlying size %d", size, rawSize)
	}

	totalSize, err := compressedStore.TotalSize(ctx)
	if err != nil {
		t.Fatalf("TotalSize failed: %v", err)
	}
	if totalSize != rawSize+rawSize2 {
		t.Errorf("Unexpected total size: %d", totalSize)
	}

	keys, err := compressedStore.List(ctx, "test/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("Expected 2 keys, got %d", len(keys))
	}

	// Delete
	if err := compressedStore.Delete(ctx, key1); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	exists, _ = compressedStore.Exists(ctx, key1)
	if exists {
		t.Errorf("Expected key to be deleted")
	}
}
