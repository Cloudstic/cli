package store_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

func TestLocalStore_SizedListerConformance(t *testing.T) {
	s, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storetest.AssertSizedListerConformance(t, s)
}

// unsizedStore is a store.ObjectStore without the capability, counting the
// Size calls the fallback makes on its behalf.
type unsizedStore struct {
	store.ObjectStore
	sizeCalls atomic.Int64
	failSize  string
}

func (s *unsizedStore) Size(ctx context.Context, key string) (int64, error) {
	s.sizeCalls.Add(1)
	if key == s.failSize {
		return 0, errors.New("size unavailable")
	}
	return s.ObjectStore.Size(ctx, key)
}

// countingSizedStore has the capability and counts Size calls, so a test can
// tell forwarding from a fallback that returns the same answers.
type countingSizedStore struct {
	*storetest.MemStore
	sizeCalls atomic.Int64
}

func (s *countingSizedStore) Size(ctx context.Context, key string) (int64, error) {
	s.sizeCalls.Add(1)
	return s.MemStore.Size(ctx, key)
}

func seedSized(t *testing.T, s store.ObjectStore, keys ...string) {
	t.Helper()
	for i, key := range keys {
		if err := s.Put(context.Background(), key, []byte(strings.Repeat("x", i+1))); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
}

func collectSized(t *testing.T, s store.ObjectStore, prefix string) map[string]int64 {
	t.Helper()
	got := map[string]int64{}
	err := store.ListSized(context.Background(), s, prefix, func(key string, size int64) error {
		got[key] = size
		return nil
	})
	if err != nil {
		t.Fatalf("ListSized: %v", err)
	}
	return got
}

// The fallback is what lets a backend opt out by not implementing the
// interface: it lists, then sizes each key, and is correct everywhere.
func TestListSized_FallsBackToListAndSize(t *testing.T) {
	inner := &unsizedStore{ObjectStore: storetest.NewMemStore()}
	seedSized(t, inner, "chunk/a", "chunk/b", "chunk/c")

	got := collectSized(t, inner, "chunk/")
	if len(got) != 3 || got["chunk/a"] != 1 || got["chunk/b"] != 2 || got["chunk/c"] != 3 {
		t.Errorf("ListSized = %v, want the three seeded sizes", got)
	}
	if n := inner.sizeCalls.Load(); n != 3 {
		t.Errorf("fallback made %d Size calls, want one per key", n)
	}
}

// A size the fallback cannot read fails the listing rather than being
// skipped: a caller lists with sizes to account for what it acts on, and
// must not act on a set it could not fully size.
func TestListSized_FallbackFailsWhenASizeCannotBeRead(t *testing.T) {
	inner := &unsizedStore{ObjectStore: storetest.NewMemStore(), failSize: "chunk/b"}
	seedSized(t, inner, "chunk/a", "chunk/b")

	err := store.ListSized(context.Background(), inner, "chunk/", func(string, int64) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "chunk/b") {
		t.Errorf("ListSized = %v, want an error naming chunk/b", err)
	}
}

// DebugStore and QuotaStore only observe, so both must forward: a --debug run
// or a budgeted backup that fell back would pay a Size per listed key and
// attribute it to the backend.
func TestWrappers_ForwardSizedListing(t *testing.T) {
	inner := &countingSizedStore{MemStore: storetest.NewMemStore()}
	seedSized(t, inner, "chunk/a", "chunk/b")

	var buf strings.Builder
	wrappers := map[string]store.ObjectStore{
		"DebugStore": store.NewDebugStore(inner, &buf),
		"QuotaStore": store.NewQuotaStore(inner, 1<<20, func(error) {}),
	}
	for name, w := range wrappers {
		t.Run(name, func(t *testing.T) {
			inner.sizeCalls.Store(0)
			got := collectSized(t, w, "chunk/")
			if len(got) != 2 || got["chunk/a"] != 1 || got["chunk/b"] != 2 {
				t.Errorf("ListSized through %s = %v", name, got)
			}
			if n := inner.sizeCalls.Load(); n != 0 {
				t.Errorf("%s fell back: the inner store answered %d Size calls", name, n)
			}
		})
	}
	if !strings.Contains(buf.String(), "LIST") {
		t.Error("DebugStore did not log the sized listing as a LIST")
	}
}

// sizedDeleteRecorder records the sizes a batch delete arrived with, so a
// test can see whether the wrappers above it kept them or dropped to keys.
type sizedDeleteRecorder struct {
	*storetest.MemStore
	sized [][]store.SizedKey
	plain [][]string
}

func (s *sizedDeleteRecorder) DeleteAll(ctx context.Context, keys []string) error {
	s.plain = append(s.plain, keys)
	return store.DeleteEach(ctx, keys, s.Delete)
}

func (s *sizedDeleteRecorder) DeleteAllSized(ctx context.Context, objects []store.SizedKey) error {
	s.sized = append(s.sized, objects)
	return store.DeleteEach(ctx, store.KeysOf(objects), s.Delete)
}

func TestWrappers_ForwardSizedDeletes(t *testing.T) {
	ctx := context.Background()
	objects := []store.SizedKey{{Key: "chunk/a", Size: 1}, {Key: "chunk/b", Size: 2}}

	build := map[string]func(store.ObjectStore) store.ObjectStore{
		"DebugStore": func(s store.ObjectStore) store.ObjectStore { return store.NewDebugStore(s, &strings.Builder{}) },
		"QuotaStore": func(s store.ObjectStore) store.ObjectStore { return store.NewQuotaStore(s, 1<<20, func(error) {}) },
	}
	for name, wrap := range build {
		t.Run(name, func(t *testing.T) {
			inner := &sizedDeleteRecorder{MemStore: storetest.NewMemStore()}
			seedSized(t, inner, "chunk/a", "chunk/b")
			if err := store.DeleteAllSized(ctx, wrap(inner), objects); err != nil {
				t.Fatalf("DeleteAllSized: %v", err)
			}
			if len(inner.sized) != 1 || len(inner.plain) != 0 {
				t.Errorf("%s dropped the sizes: sized batches %v, plain batches %v", name, inner.sized, inner.plain)
			}
			for _, key := range store.KeysOf(objects) {
				if exists, _ := inner.Exists(ctx, key); exists {
					t.Errorf("%s survived", key)
				}
			}
		})
	}
}

// Over a store without the capability the sizes are simply dropped and the
// keys go through DeleteAll, so a custom backend keeps working unchanged.
func TestDeleteAllSized_FallsBackToDeleteAll(t *testing.T) {
	ctx := context.Background()
	inner := storetest.NewMemStore()
	seedSized(t, inner, "chunk/a", "chunk/b")

	err := store.DeleteAllSized(ctx, inner, []store.SizedKey{{Key: "chunk/a", Size: 1}, {Key: "chunk/b", Size: 2}})
	if err != nil {
		t.Fatalf("DeleteAllSized: %v", err)
	}
	if inner.Len() != 0 {
		t.Errorf("%d objects survived the fallback", inner.Len())
	}
}
