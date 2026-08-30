package storelayer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cloudstic/cli/pkg/store/local"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

func TestMeteredStore(t *testing.T) {
	ctx := context.Background()

	// 1. Setup local Datastore
	tmpDir := t.TempDir()

	localStore, err := local.New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}

	meteredStore := NewMeteredStore(localStore)

	// --- 2. Test lifecycle ---
	data1 := []byte("hello metered!")
	key1 := "test/file1.txt"

	// Initial Bytes
	if bw := meteredStore.BytesWritten(); bw != 0 {
		t.Errorf("Expected initial BytesWritten to be 0, got %d", bw)
	}

	// Put
	if err := meteredStore.Put(ctx, key1, data1); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Validate bytes
	if bw := meteredStore.BytesWritten(); bw != int64(len(data1)) {
		t.Errorf("Expected BytesWritten %d, got %d", len(data1), bw)
	}

	// Index Put overrides counting
	key2 := "index/some-index.idx"
	data2 := []byte("this shouldn't count")
	if err := meteredStore.Put(ctx, key2, data2); err != nil {
		t.Fatalf("Put index failed: %v", err)
	}
	if bw := meteredStore.BytesWritten(); bw != int64(len(data1)) {
		t.Errorf("Index Put should ignore byte counting. Expected %d, got %d", len(data1), bw)
	}

	// Get
	fetched, err := meteredStore.Get(ctx, key1)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(fetched) != string(data1) {
		t.Fatalf("Get mismatch. want %q, got %q", string(data1), string(fetched))
	}

	// Exists
	exists, err := meteredStore.Exists(ctx, key1)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Errorf("Expected exists to be true")
	}

	// --- 3. Store passthrough logic ---
	size, err := meteredStore.Size(ctx, key1)
	if err != nil {
		t.Fatalf("Size failed: %v", err)
	}
	if size != int64(len(data1)) {
		t.Errorf("Store size %d doesn't match original size %d", size, len(data1))
	}

	totalSize, err := meteredStore.TotalSize(ctx)
	if err != nil {
		t.Fatalf("TotalSize failed: %v", err)
	}
	if totalSize != int64(len(data1)+len(data2)) { // Includes the index bytes over local disk
		t.Errorf("Unexpected total size: %d", totalSize)
	}

	keys, err := meteredStore.List(ctx, "test/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(keys))
	}

	// Delete and decrement logic
	deletedSize, err := meteredStore.DeleteReturnSize(ctx, key1)
	if err != nil {
		t.Fatalf("DeleteReturnSize failed: %v", err)
	}
	if deletedSize != int64(len(data1)) {
		t.Errorf("Return size doesn't match origin %d", deletedSize)
	}

	// Bytes should return to zero here since we backed out the only counted file
	if bw := meteredStore.BytesWritten(); bw != 0 {
		t.Errorf("Expected BytesWritten %d, got %d", 0, bw)
	}

	// Double Delete to ensure the `err` bubbled appropriately over a missing file
	if err := meteredStore.Delete(ctx, key1); err == nil {
		t.Fatalf("Expected Delete on missing file to report error")
	}

	exists, _ = meteredStore.Exists(ctx, key1)
	if exists {
		t.Errorf("Expected key to be deleted")
	}

	// Delete Index file (should not decrement)
	if err := meteredStore.Delete(ctx, key2); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if bw := meteredStore.BytesWritten(); bw != 0 {
		t.Errorf("Expected BytesWritten %d, got %d", 0, bw)
	}

	// Reset logic
	if err := meteredStore.Put(ctx, "test/reset.txt", data1); err != nil {
		t.Fatalf("Put reset failed: %v", err)
	}
	meteredStore.Reset()
	if bw := meteredStore.BytesWritten(); bw != 0 {
		t.Errorf("Reset should clear BytesWritten to %d, got %d", 0, bw)
	}
}

// countingRanger is an inner store that can range and records when it is
// asked to, so a test can tell forwarding from a fallback that happens to
// return the same bytes.
type countingRanger struct {
	*storetest.MemStore
	ranged atomic.Int64
}

func (c *countingRanger) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	c.ranged.Add(1)
	data, err := c.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || offset+length > int64(len(data)) {
		return nil, errors.New("range outside object")
	}
	out := make([]byte, length)
	copy(out, data[offset:offset+length])
	return out, nil
}

// The point of the method: a ranged read must reach a backend that can serve
// one. Without it a v3 blob read transfers the whole blob to get at one
// member, which is the cost the byte range exists to avoid — and it would do
// so silently, since the bytes come back correct either way.
func TestMeteredStoreForwardsRangedReads(t *testing.T) {
	ctx := context.Background()
	inner := &countingRanger{MemStore: storetest.NewMemStore()}
	m := NewMeteredStore(inner)

	if err := m.Put(ctx, "blob/abc", []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetRange(ctx, "blob/abc", 3, 4)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	if string(got) != "3456" {
		t.Fatalf("GetRange = %q, want %q", got, "3456")
	}
	if n := inner.ranged.Load(); n != 1 {
		t.Fatalf("inner store served %d ranged reads, want 1; the range fell back to a whole-object Get", n)
	}
}

// Over a backend that cannot range, the fallback must still be correct — that
// is what lets a backend opt out by not implementing the interface.
func TestMeteredStoreRangeGetterConformance(t *testing.T) {
	storetest.AssertRangeGetterConformance(t, NewMeteredStore(storetest.NewMemStore()))
}

// Metering counts bytes written; a read of any shape must not move it.
func TestMeteredStoreRangedReadDoesNotMeter(t *testing.T) {
	ctx := context.Background()
	m := NewMeteredStore(storetest.NewMemStore())
	if err := m.Put(ctx, "blob/abc", []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	before := m.BytesWritten()
	if _, err := m.GetRange(ctx, "blob/abc", 0, 4); err != nil {
		t.Fatal(err)
	}
	if after := m.BytesWritten(); after != before {
		t.Fatalf("a ranged read moved the meter from %d to %d", before, after)
	}
}
