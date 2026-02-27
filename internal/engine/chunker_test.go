package engine

import (
	"context"
	"strings"
	"testing"
)

func TestChunker_ProcessStream(t *testing.T) {
	ctx := context.Background()
	store := NewMockStore()
	chunker := NewChunker(store)

	data := "1234567890123456789012345"
	reader := strings.NewReader(data)

	refs, size, _, err := chunker.ProcessStream(reader, nil)
	if err != nil {
		t.Fatalf("ProcessStream failed: %v", err)
	}

	if size != 25 {
		t.Errorf("Expected size 25, got %d", size)
	}
	if len(refs) != 1 {
		t.Errorf("Expected 1 chunk for small data, got %d", len(refs))
	}

	// Verify chunk content (stored raw; compression is handled by the store layer)
	ref := refs[0]
	stored, _ := store.Get(ctx, ref)
	if string(stored) != data {
		t.Errorf("Chunk content mismatch")
	}
}

func TestChunker_Deduplication(t *testing.T) {
	store := NewMockStore()
	chunker := NewChunker(store)

	data := "1234567890"

	refs1, _, _, _ := chunker.ProcessStream(strings.NewReader(data), nil)
	refs2, _, _, _ := chunker.ProcessStream(strings.NewReader(data), nil)

	if len(refs1) == 0 || len(refs2) == 0 {
		t.Fatal("No chunks produced")
	}

	if refs1[0] != refs2[0] {
		t.Error("Refs should match for identical content")
	}
}

func TestChunker_CreateContentObject(t *testing.T) {
	ctx := context.Background()
	store := NewMockStore()
	chunker := NewChunker(store)

	chunks := []string{"chunk/1", "chunk/2"}
	size := int64(100)
	contentHash := "test-hash"

	ref, err := chunker.CreateContentObject(chunks, size, contentHash)
	if err != nil {
		t.Fatalf("CreateContentObject failed: %v", err)
	}

	expectedRef := "content/" + contentHash
	if ref != expectedRef {
		t.Errorf("Expected ref %s, got %s", expectedRef, ref)
	}

	data, _ := store.Get(ctx, ref)
	if !strings.Contains(string(data), "chunk/1") {
		t.Error("Content object missing chunk/1")
	}
}
