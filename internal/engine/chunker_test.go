package engine

import (
	"context"
	"strings"
	"testing"
)

func TestChunker_ProcessStream(t *testing.T) {
	ctx := context.Background()
	store := NewMockStore()
	chunker := NewChunker(store, nil)

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
	chunker := NewChunker(store, nil)

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
	chunker := NewChunker(store, nil)

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

// TestChunker_HMAC_ChunkRefsUseHMAC verifies that providing an HMAC key
// produces different chunk refs than without one, while the content hash
// (stream SHA-256) remains identical.
func TestChunker_HMAC_ChunkRefsUseHMAC(t *testing.T) {
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	data := "deterministic test payload"

	// Without HMAC key
	plainStore := NewMockStore()
	plainChunker := NewChunker(plainStore, nil)
	plainRefs, _, plainHash, err := plainChunker.ProcessStream(strings.NewReader(data), nil)
	if err != nil {
		t.Fatalf("plain ProcessStream failed: %v", err)
	}

	// With HMAC key
	hmacStore := NewMockStore()
	hmacChunker := NewChunker(hmacStore, hmacKey)
	hmacRefs, _, hmacHash, err := hmacChunker.ProcessStream(strings.NewReader(data), nil)
	if err != nil {
		t.Fatalf("hmac ProcessStream failed: %v", err)
	}

	// Content hash must be identical (always plain SHA-256)
	if plainHash != hmacHash {
		t.Errorf("content hash must not change with HMAC key:\n  plain: %s\n  hmac:  %s", plainHash, hmacHash)
	}

	// Chunk refs must differ (HMAC vs plain SHA-256)
	if len(plainRefs) == 0 || len(hmacRefs) == 0 {
		t.Fatal("no chunks produced")
	}
	if plainRefs[0] == hmacRefs[0] {
		t.Error("chunk refs should differ when HMAC key is used")
	}
}

// TestChunker_HMAC_ContentHashStable verifies that the content hash returned
// by ProcessStream is deterministic and independent of the HMAC key. This
// prevents the regression where HMAC-ing the stream hash caused subsequent
// backups to detect false changes.
func TestChunker_HMAC_ContentHashStable(t *testing.T) {
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")
	data := "stable content hash test"

	// Run ProcessStream twice with the same HMAC key
	s1 := NewMockStore()
	_, _, hash1, _ := NewChunker(s1, hmacKey).ProcessStream(strings.NewReader(data), nil)

	s2 := NewMockStore()
	_, _, hash2, _ := NewChunker(s2, hmacKey).ProcessStream(strings.NewReader(data), nil)

	if hash1 != hash2 {
		t.Errorf("content hash not stable across runs: %s vs %s", hash1, hash2)
	}

	// Run without HMAC key — content hash must still match
	s3 := NewMockStore()
	_, _, hash3, _ := NewChunker(s3, nil).ProcessStream(strings.NewReader(data), nil)

	if hash1 != hash3 {
		t.Errorf("content hash must be identical with/without HMAC key: %s vs %s", hash1, hash3)
	}
}

// TestChunker_HMAC_Deduplication verifies that dedup works correctly when
// an HMAC key is used — identical data produces identical chunk refs.
func TestChunker_HMAC_Deduplication(t *testing.T) {
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")
	store := NewMockStore()
	chunker := NewChunker(store, hmacKey)

	data := "dedup with hmac test"

	refs1, _, _, _ := chunker.ProcessStream(strings.NewReader(data), nil)
	refs2, _, _, _ := chunker.ProcessStream(strings.NewReader(data), nil)

	if len(refs1) == 0 || len(refs2) == 0 {
		t.Fatal("no chunks produced")
	}
	if refs1[0] != refs2[0] {
		t.Errorf("HMAC dedup failed: refs differ for identical content\n  run1: %s\n  run2: %s", refs1[0], refs2[0])
	}
}
