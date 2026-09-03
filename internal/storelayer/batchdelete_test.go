package storelayer

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
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

// sizeCountingStore counts Size calls: the request a sweep pays per object
// when the listing's sizes do not reach the meter.
type sizeCountingStore struct {
	*batchMemStore
	sizeCalls atomic.Int64
}

func (s *sizeCountingStore) Size(ctx context.Context, key string) (int64, error) {
	s.sizeCalls.Add(1)
	return s.batchMemStore.Size(ctx, key)
}

func listSized(t *testing.T, s store.ObjectStore, prefix string) []store.SizedKey {
	t.Helper()
	var objects []store.SizedKey
	err := store.ListSized(context.Background(), s, prefix, func(key string, size int64) error {
		objects = append(objects, store.SizedKey{Key: key, Size: size})
		return nil
	})
	if err != nil {
		t.Fatalf("ListSized: %v", err)
	}
	return objects
}

// The point of DeleteListed: a sweep that has just listed its keys with their
// sizes credits from that listing and asks the store for nothing per key.
func TestMeteredStore_DeleteListedAsksForNoSizes(t *testing.T) {
	ctx := context.Background()
	backend := &sizeCountingStore{batchMemStore: newBatchMemStore()}
	seed(t, backend, "chunk/a", "chunk/b", "chunk/c")

	m := NewMeteredStore(backend)
	objects := listSized(t, m, "chunk/")
	if len(objects) != 3 {
		t.Fatalf("listed %v, want three objects", objects)
	}
	sizes, err := m.DeleteListed(ctx, objects)
	if err != nil {
		t.Fatalf("DeleteListed: %v", err)
	}
	if len(sizes) != 3 {
		t.Errorf("confirmed deletions = %v, want all three", sizes)
	}
	if got := m.BytesWritten(); got != -30 {
		t.Errorf("credited %d bytes, want -30 (three objects of ten)", got)
	}
	if n := backend.sizeCalls.Load(); n != 0 {
		t.Errorf("the store answered %d Size calls, want 0: the listing already said", n)
	}
}

// The accounting rule does not loosen because the size came from a listing: a
// refused key is still not credited, and nothing is credited when the store
// gives no per-key detail.
func TestMeteredStore_DeleteListedCreditsOnlyConfirmedKeys(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()
	seed(t, backend, "chunk/a", "chunk/b", "chunk/c")
	backend.refuse["chunk/b"] = true

	m := NewMeteredStore(backend)
	sizes, err := m.DeleteListed(ctx, listSized(t, m, "chunk/"))
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

func TestMeteredStore_DeleteListedCreditsNothingWithoutPerKeyDetail(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()
	backend.opaque = true
	seed(t, backend, "chunk/a", "chunk/b")

	m := NewMeteredStore(backend)
	sizes, err := m.DeleteListed(ctx, listSized(t, m, "chunk/"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(sizes) != 0 {
		t.Errorf("confirmed deletions = %v, want none", sizes)
	}
	if got := m.BytesWritten(); got != 0 {
		t.Errorf("credited %d bytes, want 0", got)
	}
	failed, ok := store.FailedDeletes(err)
	if !ok || len(failed) != 2 {
		t.Errorf("FailedDeletes = (%v, %v), want both keys", failed, ok)
	}
}

// index/ objects are not metered on the way in, so a listing that reports
// their true size must not have them credited on the way out.
func TestMeteredStore_DeleteListedDoesNotCreditIndexObjects(t *testing.T) {
	ctx := context.Background()
	backend := newBatchMemStore()
	seed(t, backend, "chunk/a", "index/packs")

	m := NewMeteredStore(backend)
	objects := []store.SizedKey{{Key: "chunk/a", Size: 10}, {Key: "index/packs", Size: 10}}
	sizes, err := m.DeleteListed(ctx, objects)
	if err != nil {
		t.Fatalf("DeleteListed: %v", err)
	}
	if size, ok := sizes["index/packs"]; !ok || size != 0 {
		t.Errorf("index/packs reported as (%d, %v), want confirmed at zero credit", size, ok)
	}
	if got := m.BytesWritten(); got != -10 {
		t.Errorf("credited %d bytes, want -10 (the chunk only)", got)
	}
}

// A prune's sweep meters through one MeteredStore and the client meters
// through another beneath the encryption layer. The listing's sizes have to
// reach both, or the inner one asks the store for every key and the sweep
// pays the round trips the listing was meant to save — which is exactly what
// happened: two Size calls per object, one per meter.
func TestChain_ListedSizesReachEveryMeterWithoutASizeCall(t *testing.T) {
	ctx := context.Background()
	backend := &sizeCountingStore{batchMemStore: newBatchMemStore()}

	inner := NewMeteredStore(backend)
	chain := NewCompressedStore(
		NewEncryptedStore(inner, make([]byte, 32)),
		WithFramedWrites(true),
	)
	keys := []string{"chunk/a", "chunk/b", "chunk/c"}
	seed(t, chain, keys...)
	inner.Reset()

	outer := NewMeteredStore(chain)
	objects := listSized(t, outer, "chunk/")
	if len(objects) != len(keys) {
		t.Fatalf("listed %v through the chain, want %v", objects, keys)
	}
	sizes, err := outer.DeleteListed(ctx, objects)
	if err != nil {
		t.Fatalf("DeleteListed: %v", err)
	}
	if len(sizes) != len(keys) {
		t.Errorf("confirmed deletions = %v, want all of %v", sizes, keys)
	}
	if n := backend.sizeCalls.Load(); n != 0 {
		t.Errorf("the backend answered %d Size calls, want 0 from either meter", n)
	}
	if len(backend.batches) != 1 || len(backend.batches[0]) != len(keys) {
		t.Fatalf("backend saw %v, want one batch of %d keys", backend.batches, len(keys))
	}
	// Both meters credited the same stored bytes: what the listing reported.
	var stored int64
	for _, o := range objects {
		stored += o.Size
	}
	if got := outer.BytesWritten(); got != -stored {
		t.Errorf("outer meter credited %d, want -%d", got, stored)
	}
	if got := inner.BytesWritten(); got != -stored {
		t.Errorf("inner meter credited %d, want -%d", got, stored)
	}
}

// PackStore does not claim the sized delete either, for the reason it does
// not claim the plain one: most of its keys are not backend objects.
func TestPackStoreDoesNotClaimSizedBatchDelete(t *testing.T) {
	pack, err := NewPackStore(storetest.NewMemStore())
	if err != nil {
		t.Fatalf("new pack store: %v", err)
	}
	if _, ok := any(pack).(store.SizedBatchDeleter); ok {
		t.Fatal("PackStore must not implement store.SizedBatchDeleter: a batched delete " +
			"forwarded to the backend would not touch its packed entries")
	}
}

// A packed repository lists with sizes the way List and Size answer together:
// packed keys at their catalog length, unpacked ones at the backend's size,
// and never the packfiles themselves.
func TestPackStore_ListSizedAnswersLikeListAndSize(t *testing.T) {
	ctx := context.Background()
	backend := &sizeCountingStore{batchMemStore: newBatchMemStore()}
	pack, err := NewPackStore(backend)
	if err != nil {
		t.Fatalf("new pack store: %v", err)
	}
	if err := pack.Put(ctx, "chunk/small", []byte(strings.Repeat("s", 100))); err != nil {
		t.Fatal(err)
	}
	if err := pack.Put(ctx, "chunk/large", make([]byte, maxObjectSize+1)); err != nil {
		t.Fatal(err)
	}
	if err := pack.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	objects := listSized(t, pack, "chunk/")
	// The whole listing costs the pack store no Size request: the packed key
	// is answered from the catalog and the unpacked one from the backend's
	// own listing. Counted here, before the comparison below asks Size itself.
	if n := backend.sizeCalls.Load(); n != 0 {
		t.Errorf("the backend answered %d Size calls during ListSized, want 0", n)
	}
	listed, err := pack.List(ctx, "chunk/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != len(listed) || len(objects) != 2 {
		t.Fatalf("ListSized = %v, List = %v, want the same two keys", objects, listed)
	}
	for _, o := range objects {
		want, err := pack.Size(ctx, o.Key)
		if err != nil {
			t.Fatal(err)
		}
		if o.Size != want {
			t.Errorf("%s listed at %d bytes, Size reports %d", o.Key, o.Size, want)
		}
	}
	// And nothing under packs/ leaks through as an object.
	for _, o := range listSized(t, pack, "") {
		if strings.HasPrefix(o.Key, packPrefix) {
			t.Errorf("ListSized listed the packfile %s as an object", o.Key)
		}
	}
}
