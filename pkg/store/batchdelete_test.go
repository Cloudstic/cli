package store_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

func TestLocalStore_BatchDeleterConformance(t *testing.T) {
	s, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storetest.AssertBatchDeleterConformance(t, s)
}

// DebugStore must satisfy the contract over a backend that batches and over one
// that does not: --debug is a logging wrapper, and turning it on must not
// change what a caller can do with the store underneath.
func TestDebugStore_BatchDeleterConformance(t *testing.T) {
	t.Run("over a backend that batches", func(t *testing.T) {
		inner, err := local.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		storetest.AssertBatchDeleterConformance(t, store.NewDebugStore(inner, &strings.Builder{}))
	})

	t.Run("over a backend that does not", func(t *testing.T) {
		inner, err := local.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		plain := &nonBatchStore{ObjectStore: inner}
		storetest.AssertBatchDeleterConformance(t, store.NewDebugStore(plain, &strings.Builder{}))
	})
}

func TestQuotaStore_BatchDeleterConformance(t *testing.T) {
	inner, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	storetest.AssertBatchDeleterConformance(t, store.NewQuotaStore(inner, 1<<30, cancel))
}

// nonBatchStore hides a backend's BatchDeleter, so a wrapper's fallback path is
// exercised rather than inherited.
type nonBatchStore struct{ store.ObjectStore }

// countingBatchStore records the batch sizes it was asked for, so a test can
// tell "one request per thousand keys" from "one request per key".
type countingBatchStore struct {
	store.ObjectStore
	mu      sync.Mutex
	batches []int
	singles int
}

func (c *countingBatchStore) DeleteAll(ctx context.Context, keys []string) error {
	c.mu.Lock()
	c.batches = append(c.batches, len(keys))
	c.mu.Unlock()
	return store.DeleteEach(ctx, keys, c.ObjectStore.Delete)
}

func (c *countingBatchStore) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	c.singles++
	c.mu.Unlock()
	return c.ObjectStore.Delete(ctx, key)
}

func TestDeleteAll_PrefersTheBatchCapability(t *testing.T) {
	ctx := context.Background()
	inner, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingBatchStore{ObjectStore: inner}

	keys := []string{"chunk/a", "chunk/b", "chunk/c"}
	for _, k := range keys {
		if err := counting.Put(ctx, k, []byte(k)); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.DeleteAll(ctx, counting, keys); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if len(counting.batches) != 1 || counting.batches[0] != len(keys) {
		t.Errorf("expected one batch of %d keys, got %v", len(keys), counting.batches)
	}
}

// A store that cannot batch must still be usable through DeleteAll, or every
// caller would need its own fallback branch.
func TestDeleteAll_LoopsWhenTheStoreCannotBatch(t *testing.T) {
	ctx := context.Background()
	inner, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"chunk/a", "chunk/b"}
	for _, k := range keys {
		if err := inner.Put(ctx, k, []byte(k)); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.DeleteAll(ctx, &nonBatchStore{ObjectStore: inner}, keys); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	for _, k := range keys {
		if exists, _ := inner.Exists(ctx, k); exists {
			t.Errorf("%s survived", k)
		}
	}
}

// The lookup must not unwrap: PackStore's Delete rewrites a catalog rather than
// touching the backend, so reaching past a wrapper to a batch-capable store
// beneath it would report deletions that never happened.
func TestDeleteAll_DoesNotUnwrapPastAWrapper(t *testing.T) {
	ctx := context.Background()
	inner, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingBatchStore{ObjectStore: inner}

	keys := []string{"chunk/a", "chunk/b"}
	for _, k := range keys {
		if err := counting.Put(ctx, k, []byte(k)); err != nil {
			t.Fatal(err)
		}
	}

	// nonBatchStore stands in for a wrapper that means something by a delete
	// and so does not claim the capability.
	if err := store.DeleteAll(ctx, &nonBatchStore{ObjectStore: counting}, keys); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if len(counting.batches) != 0 {
		t.Errorf("DeleteAll reached the inner batch capability: %v", counting.batches)
	}
	if counting.singles != len(keys) {
		t.Errorf("expected %d single deletes through the wrapper, got %d", len(keys), counting.singles)
	}
}

func TestDeleteEach_ReportsEveryFailureNotJustTheFirst(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("refused")
	keys := []string{"chunk/a", "chunk/b", "chunk/c", "chunk/d"}

	err := store.DeleteEach(ctx, keys, func(_ context.Context, key string) error {
		if key == "chunk/a" || key == "chunk/c" {
			return boom
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected an error")
	}

	failed, ok := store.FailedDeletes(err)
	if !ok {
		t.Fatal("DeleteEach must report failures per key")
	}
	if got := failed.Keys(); len(got) != 2 || got[0] != "chunk/a" || got[1] != "chunk/c" {
		t.Errorf("failed keys = %v, want [chunk/a chunk/c]", got)
	}
	if !errors.Is(err, boom) {
		t.Errorf("the cause should stay inspectable, got %v", err)
	}
}

// A key that is already gone counts as deleted. S3's DeleteObjects reports a
// missing key as deleted while os.Remove and sftp Remove both error, and
// letting that difference through would mean a prune that succeeds on S3 and
// fails on a local store after an interrupted run.
func TestDeleteEach_TreatsAMissingObjectAsDeleted(t *testing.T) {
	err := store.DeleteEach(context.Background(), []string{"chunk/a", "chunk/b"},
		func(_ context.Context, key string) error {
			if key == "chunk/a" {
				// What os.Remove and sftp Remove return for a missing path.
				return &fs.PathError{Op: "remove", Path: key, Err: fs.ErrNotExist}
			}
			return fmt.Errorf("%s: %w", key, store.ErrNotFound)
		})
	if err != nil {
		t.Errorf("a missing object must not be a failure, got %v", err)
	}
}

func TestDeleteEach_ReportsTheRemainderWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	keys := []string{"chunk/a", "chunk/b", "chunk/c"}
	err := store.DeleteEach(ctx, keys, func(context.Context, string) error {
		t.Error("no delete should be attempted on a cancelled context")
		return nil
	})

	failed, ok := store.FailedDeletes(err)
	if !ok {
		t.Fatalf("expected per-key failures, got %v", err)
	}
	if len(failed) != len(keys) {
		t.Errorf("every key is unconfirmed, got %v", failed.Keys())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the cause should stay inspectable, got %v", err)
	}
}

// "No keys failed" and "which keys failed is unknown" are opposite answers for
// a garbage collector, which is why FailedDeletes has a second return value.
func TestFailedDeletes_DistinguishesUnknownFromNone(t *testing.T) {
	if keys, ok := store.FailedDeletes(nil); !ok || len(keys) != 0 {
		t.Errorf("nil error = (%v, %v), want (empty, true)", keys, ok)
	}

	opaque := errors.New("the request never landed")
	if keys, ok := store.FailedDeletes(opaque); ok || keys != nil {
		t.Errorf("an error with no per-key detail must report ok=false, got (%v, %v)", keys, ok)
	}

	detailed := store.DeleteErrors{{Key: "chunk/a", Err: opaque}}
	keys, ok := store.FailedDeletes(detailed)
	if !ok || len(keys) != 1 || keys[0].Key != "chunk/a" {
		t.Errorf("FailedDeletes = (%v, %v)", keys, ok)
	}

	// Wrapping must not hide the detail, since a store may add context.
	wrapped := errors.Join(errors.New("sweep"), detailed)
	if keys, ok := store.FailedDeletes(wrapped); !ok || len(keys) != 1 {
		t.Errorf("wrapped DeleteErrors = (%v, %v)", keys, ok)
	}
}

func TestDeleteErrors_SummarisesWithoutListingEveryKey(t *testing.T) {
	boom := errors.New("refused")
	var many store.DeleteErrors
	for _, k := range []string{"chunk/a", "chunk/b", "chunk/c"} {
		many = append(many, store.DeleteError{Key: k, Err: boom})
	}
	msg := many.Error()
	if !strings.Contains(msg, "chunk/a") || !strings.Contains(msg, "2 more") {
		t.Errorf("Error() = %q; want the first key and a count of the rest", msg)
	}
	if strings.Contains(msg, "chunk/c") {
		t.Errorf("Error() should not list every key: %q", msg)
	}
}
