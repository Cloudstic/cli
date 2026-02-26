package engine

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestChunker_ProcessStream(t *testing.T) {
	store := NewMockStore()
	chunker := NewChunker(store)
	// Note: With FastCDC, exact chunk boundaries are content-defined and depend on Min/Avg/Max sizes.
	// The configured MinSize is 512KB.
	// If data is smaller than MinSize, it should be one chunk.

	// Test Case 1: Small file (smaller than MinSize 512KB)
	data := "1234567890123456789012345"
	reader := strings.NewReader(data)

	refs, size, newBytes, newBytesCompressed, _, err := chunker.ProcessStream(reader, nil)
	if err != nil {
		t.Fatalf("ProcessStream failed: %v", err)
	}

	if size != 25 {
		t.Errorf("Expected size 25, got %d", size)
	}
	if len(refs) != 1 {
		t.Errorf("Expected 1 chunk for small data, got %d", len(refs))
	}
	if newBytes != 25 {
		t.Errorf("Expected newBytes 25, got %d", newBytes)
	}
	if newBytesCompressed <= 0 {
		t.Errorf("Expected positive newBytesCompressed, got %d", newBytesCompressed)
	}

	// Verify chunk content
	ref := refs[0]
	compressedData, _ := store.Get(ref)
	gr, _ := gzip.NewReader(bytes.NewReader(compressedData))
	uncompressed, _ := io.ReadAll(gr)
	_ = gr.Close()

	if string(uncompressed) != data {
		t.Errorf("Chunk content mismatch")
	}

	// Test Case 2: Large random data to trigger splitting?
	// Generating 2MB of data should trigger split if Avg is 1MB.
	// But FastCDC is probabilistic.
	// We can trust the library works, just verify we handle multiple chunks correctly.

	// We can't easily predict exact chunk counts without knowing the polynomial and data.
	// But we can verify we get > 0 chunks and total size matches.
}

func TestChunker_Deduplication(t *testing.T) {
	store := NewMockStore()
	chunker := NewChunker(store)

	data := "1234567890"

	// Process once
	refs1, _, newBytes1, _, _, _ := chunker.ProcessStream(strings.NewReader(data), nil)

	// Process again -- identical data should produce zero new bytes
	refs2, _, newBytes2, newCompressed2, _, _ := chunker.ProcessStream(strings.NewReader(data), nil)

	if len(refs1) == 0 || len(refs2) == 0 {
		t.Fatal("No chunks produced")
	}

	if refs1[0] != refs2[0] {
		t.Error("Refs should match for identical content")
	}
	if newBytes1 <= 0 {
		t.Error("First process should report positive newBytes")
	}
	if newBytes2 != 0 {
		t.Errorf("Second process should report 0 newBytes (deduped), got %d", newBytes2)
	}
	if newCompressed2 != 0 {
		t.Errorf("Second process should report 0 newBytesCompressed (deduped), got %d", newCompressed2)
	}
}

func TestChunker_CreateContentObject(t *testing.T) {
	store := NewMockStore()
	chunker := NewChunker(store)

	chunks := []string{"chunk/1", "chunk/2"}
	size := int64(100)
	contentHash := "test-hash"

	ref, err := chunker.CreateContentObject(chunks, size, contentHash)
	if err != nil {
		t.Fatalf("CreateContentObject failed: %v", err)
	}

	// Verify object exists
	// The ref should be "content/" + contentHash
	expectedRef := "content/" + contentHash
	if ref != expectedRef {
		t.Errorf("Expected ref %s, got %s", expectedRef, ref)
	}

	data, _ := store.Get(ref)
	// Check if it contains chunks list
	if !strings.Contains(string(data), "chunk/1") {
		t.Error("Content object missing chunk/1")
	}
}
