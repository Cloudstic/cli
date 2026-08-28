package storelayer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

// batchMemStore is a MemStore that batches, recording the batches it was given
// so a test can tell one request per thousand keys from one per key.
type batchMemStore struct {
	*storetest.MemStore
	batches [][]string
	// refuse names keys the store reports as not deleted, and reason is what it
	// says about them. A refused key is left in place.
	refuse map[string]bool
	// opaque makes the store collapse its failures into a single error with no
	// per-key detail — what a transport failure looks like.
	opaque bool
}

var errRefused = errors.New("refused")

func (b *batchMemStore) DeleteAll(ctx context.Context, keys []string) error {
	b.batches = append(b.batches, append([]string(nil), keys...))
	if b.opaque {
		return errRefused
	}
	var failures store.DeleteErrors
	for _, key := range keys {
		if b.refuse[key] {
			failures = append(failures, store.DeleteError{Key: key, Err: errRefused})
			continue
		}
		if err := b.Delete(ctx, key); err != nil {
			failures = append(failures, store.DeleteError{Key: key, Err: err})
		}
	}
	if len(failures) > 0 {
		return failures
	}
	return nil
}

func newBatchMemStore() *batchMemStore {
	return &batchMemStore{MemStore: storetest.NewMemStore(), refuse: map[string]bool{}}
}

func seed(t *testing.T, s store.ObjectStore, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if err := s.Put(context.Background(), key, []byte(strings.Repeat("x", 10))); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
}

// The meter may credit back only the objects the store confirmed gone. A
// refused key that still counted would make prune report space it did not
// reclaim, which is the failure docs/compatibility.md's rule is about.
func TestMeteredStore_DeleteAllCreditsOnlyConfirmedKeys(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()
	seed(t, backend, "chunk/a", "chunk/b", "chunk/c")
	backend.refuse["chunk/b"] = true

	m := NewMeteredStore(backend)
	sizes, err := m.DeleteAllReturnSizes(ctx, []string{"chunk/a", "chunk/b", "chunk/c"})
	if err == nil {
		t.Fatal("a refused key must not be reported as success")
	}
	if _, ok := sizes["chunk/b"]; ok {
		t.Error("the refused key was reported as deleted")
	}
	if len(sizes) != 2 {
		t.Errorf("confirmed deletions = %v, want chunk/a and chunk/c", sizes)
	}
	if got := m.BytesWritten(); got != -20 {
		t.Errorf("credited %d bytes, want -20 (two objects of ten)", got)
	}
	if exists, _ := backend.Exists(ctx, "chunk/b"); !exists {
		t.Error("the refused key should still be there")
	}
}

// With no per-key detail nothing in the batch is confirmed, so nothing may be
// credited. Understating the space reclaimed is the safe direction; claiming a
// thousand deletions because most of them probably worked is not.
func TestMeteredStore_DeleteAllCreditsNothingWithoutPerKeyDetail(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()
	backend.opaque = true
	seed(t, backend, "chunk/a", "chunk/b")

	m := NewMeteredStore(backend)
	sizes, err := m.DeleteAllReturnSizes(ctx, []string{"chunk/a", "chunk/b"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(sizes) != 0 {
		t.Errorf("confirmed deletions = %v, want none", sizes)
	}
	if got := m.BytesWritten(); got != 0 {
		t.Errorf("credited %d bytes, want 0", got)
	}
	// The failure is still reported per key, so a caller can name what it could
	// not account for even when the store could not.
	failed, ok := store.FailedDeletes(err)
	if !ok || len(failed) != 2 {
		t.Errorf("FailedDeletes = (%v, %v), want both keys", failed, ok)
	}
}

// A key whose size cannot be read is not deleted at all: deleting bytes the
// meter cannot count would overstate the reclaimed total, and that is the
// direction that must never happen.
func TestMeteredStore_DeleteAllSkipsAKeyWhoseSizeIsUnreadable(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()
	seed(t, backend, "chunk/a")

	m := NewMeteredStore(backend)
	sizes, err := m.DeleteAllReturnSizes(ctx, []string{"chunk/a", "chunk/missing"})
	if err == nil {
		t.Fatal("expected an error for the unsizeable key")
	}
	if _, ok := sizes["chunk/missing"]; ok {
		t.Error("a key that was never sized must not be reported as deleted")
	}
	if len(backend.batches) != 1 || len(backend.batches[0]) != 1 {
		t.Errorf("only the sized key should have been sent, got %v", backend.batches)
	}
}

// index/ objects are not metered on the way in, so they must not be credited on
// the way out either.
func TestMeteredStore_DeleteAllDoesNotCreditIndexObjects(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()
	seed(t, backend, "chunk/a", "index/packs")

	m := NewMeteredStore(backend)
	if _, err := m.DeleteAllReturnSizes(ctx, []string{"chunk/a", "index/packs"}); err != nil {
		t.Fatalf("DeleteAllReturnSizes: %v", err)
	}
	if got := m.BytesWritten(); got != -10 {
		t.Errorf("credited %d bytes, want -10 (the chunk only)", got)
	}
}

// The capability has to survive the whole decorator chain, or prune batches
// against a backend that never sees a batch.
func TestChain_ForwardsBatchDeleteToTheBackend(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()

	chain := NewCompressedStore(
		NewEncryptedStore(NewMeteredStore(backend), make([]byte, 32)),
		WithFramedWrites(true),
	)
	keys := []string{"chunk/a", "chunk/b", "chunk/c"}
	seed(t, chain, keys...)

	outer := NewMeteredStore(chain)
	if _, err := outer.DeleteAllReturnSizes(ctx, keys); err != nil {
		t.Fatalf("DeleteAllReturnSizes: %v", err)
	}
	if len(backend.batches) != 1 || len(backend.batches[0]) != len(keys) {
		t.Fatalf("backend saw %v, want one batch of %d keys", backend.batches, len(keys))
	}
	for _, key := range keys {
		if exists, _ := backend.Exists(ctx, key); exists {
			t.Errorf("%s survived", key)
		}
	}
}

// PackStore must not claim the capability. Most keys it is asked to delete are
// entries in a packfile rather than objects in the backend, so a bulk delete
// forwarded past it would remove nothing and report everything.
func TestPackStoreDoesNotClaimBatchDelete(t *testing.T) {
	pack, err := NewPackStore(storetest.NewMemStore())
	if err != nil {
		t.Fatalf("new pack store: %v", err)
	}
	if _, ok := any(pack).(store.BatchDeleter); ok {
		t.Fatal("PackStore must not implement store.BatchDeleter: a batched delete " +
			"forwarded to the backend would not touch its packed entries")
	}
}

// With PackStore in the chain the capability lookup stops above it and the
// deletes arrive one key at a time — which is the correct outcome, not a
// degradation to work around.
func TestChain_DeletesPackedObjectsOneKeyAtATime(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()
	pack, err := NewPackStore(backend)
	if err != nil {
		t.Fatalf("new pack store: %v", err)
	}

	chain := NewCompressedStore(NewEncryptedStore(NewMeteredStore(pack), make([]byte, 32)))
	keys := []string{"chunk/a", "chunk/b"}
	seed(t, chain, keys...)
	if err := chain.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	outer := NewMeteredStore(chain)
	sizes, err := outer.DeleteAllReturnSizes(ctx, keys)
	if err != nil {
		t.Fatalf("DeleteAllReturnSizes: %v", err)
	}
	if len(sizes) != len(keys) {
		t.Errorf("confirmed deletions = %v, want both keys", sizes)
	}
	for _, b := range backend.batches {
		if len(b) > 1 {
			t.Errorf("a batch reached the backend past PackStore: %v", b)
		}
	}
	for _, key := range keys {
		if exists, _ := chain.Exists(ctx, key); exists {
			t.Errorf("%s survived", key)
		}
	}
}
