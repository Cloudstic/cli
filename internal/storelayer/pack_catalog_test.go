package storelayer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// faultyCatalogStore fails reads of the pack catalog a bounded number of times,
// standing in for a transient backend error (throttling, reset connection).
type faultyCatalogStore struct {
	store.ObjectStore
	failsLeft int
	injected  int
}

var errCatalogUnavailable = errors.New("RequestError: connection reset by peer")

// isIndexKey reports whether a key is part of the pack index — a shard, or the
// legacy monolithic catalog. The index is sharded, so faulting only
// indexPacksKey would leave the real load path untouched and the tests below
// passing for the wrong reason.
func isIndexKey(key string) bool {
	return key == indexPacksKey || strings.HasPrefix(key, shardPrefix)
}

func (f *faultyCatalogStore) Get(ctx context.Context, key string) ([]byte, error) {
	if isIndexKey(key) && f.failsLeft > 0 {
		f.failsLeft--
		f.injected++
		return nil, errCatalogUnavailable
	}
	return f.ObjectStore.Get(ctx, key)
}

func (f *faultyCatalogStore) Exists(ctx context.Context, key string) (bool, error) {
	if isIndexKey(key) && f.failsLeft > 0 {
		f.failsLeft--
		f.injected++
		return false, errCatalogUnavailable
	}
	return f.ObjectStore.Exists(ctx, key)
}

// List is faulted too: loading the index lists the shard prefix first, so a
// backend that cannot be reached fails there before any Get is attempted.
func (f *faultyCatalogStore) List(ctx context.Context, prefix string) ([]string, error) {
	if prefix == shardPrefix && f.failsLeft > 0 {
		f.failsLeft--
		f.injected++
		return nil, errCatalogUnavailable
	}
	return f.ObjectStore.List(ctx, prefix)
}

func newCatalogTestStore(t *testing.T) (*faultyCatalogStore, store.ObjectStore) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "cloudstic-pack-catalog-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	localStore, err := local.New(tmpDir)
	if err != nil {
		t.Fatalf("create local store: %v", err)
	}
	return &faultyCatalogStore{ObjectStore: localStore}, localStore
}

func newPackStoreT(t *testing.T, inner store.ObjectStore) *PackStore {
	t.Helper()
	ps, err := NewPackStore(inner)
	if err != nil {
		t.Fatalf("init pack store: %v", err)
	}
	return ps
}

// seedPackedRepo writes keys through a healthy PackStore and flushes them.
func seedPackedRepo(t *testing.T, inner store.ObjectStore, keys ...string) {
	t.Helper()
	ctx := context.Background()
	ps := newPackStoreT(t, inner)
	for _, key := range keys {
		if err := ps.Put(ctx, key, []byte("payload for "+key)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatalf("seed flush: %v", err)
	}
}

// A catalog that cannot be read must fail the listing. Returning a short list
// tells prune's mark phase that every packed object is unreachable.
func TestPackStore_ListFailsWhenCatalogUnreadable(t *testing.T) {
	ctx := context.Background()
	faulty, inner := newCatalogTestStore(t)
	seedPackedRepo(t, inner, "snapshot/aaa", "snapshot/bbb")

	faulty.failsLeft = 1
	ps := newPackStoreT(t, faulty)

	keys, err := ps.List(ctx, "snapshot/")
	if err == nil {
		t.Fatalf("List succeeded with %d keys; want an error when the catalog is unreadable", len(keys))
	}
	if faulty.injected == 0 {
		t.Fatal("fault was never injected")
	}
	if !strings.Contains(err.Error(), "index/packs") {
		t.Errorf("error should name the catalog, got: %v", err)
	}
}

// The same guarantee for point reads: a packed object must not be reported
// missing just because the catalog could not be consulted.
func TestPackStore_GetFailsWhenCatalogUnreadable(t *testing.T) {
	ctx := context.Background()
	faulty, inner := newCatalogTestStore(t)
	seedPackedRepo(t, inner, "filemeta/a")

	faulty.failsLeft = 1
	ps := newPackStoreT(t, faulty)

	if _, err := ps.Get(ctx, "filemeta/a"); err == nil {
		t.Fatal("Get succeeded; want an error when the catalog is unreadable")
	}
}

// Repack condemns packs whose entries are absent from the catalog, so it must
// refuse to run at all rather than treat an unreadable catalog as empty.
func TestPackStore_RepackFailsWhenCatalogUnreadable(t *testing.T) {
	ctx := context.Background()
	faulty, inner := newCatalogTestStore(t)
	seedPackedRepo(t, inner, "filemeta/a", "filemeta/b")

	packsBefore, _ := inner.List(ctx, packPrefix)

	faulty.failsLeft = 1
	ps := newPackStoreT(t, faulty)

	if _, _, err := ps.Repack(ctx, 0.3); err == nil {
		t.Fatal("Repack succeeded; want an error when the catalog is unreadable")
	}

	packsAfter, _ := inner.List(ctx, packPrefix)
	if len(packsAfter) != len(packsBefore) {
		t.Errorf("Repack deleted packs despite an unreadable catalog: %d -> %d", len(packsBefore), len(packsAfter))
	}
}

// Flush writes the catalog back wholesale. If the stored catalog was never
// merged in, flushing would strand every previously packed object.
func TestPackStore_FlushDoesNotOverwriteUnloadedCatalog(t *testing.T) {
	ctx := context.Background()
	faulty, inner := newCatalogTestStore(t)
	seedPackedRepo(t, inner, "filemeta/old1", "filemeta/old2", "snapshot/old")

	// A session whose catalog load fails, which then writes and flushes.
	faulty.failsLeft = 99
	ps := newPackStoreT(t, faulty)
	if err := ps.Put(ctx, "filemeta/new", []byte("new run")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := ps.Flush(ctx); err == nil {
		t.Fatal("Flush succeeded; want a refusal to overwrite an unloaded catalog")
	}

	// A healthy session must still see the historical entries.
	recovered := newPackStoreT(t, inner)
	keys, err := recovered.List(ctx, "filemeta/")
	if err != nil {
		t.Fatalf("List after recovery: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 historical filemeta entries, got %v", keys)
	}
	if _, err := recovered.Get(ctx, "filemeta/old1"); err != nil {
		t.Errorf("historical entry became unreadable: %v", err)
	}
	if _, err := recovered.Get(ctx, "snapshot/old"); err != nil {
		t.Errorf("historical snapshot became unreadable: %v", err)
	}
}

// Once the backend recovers, the catalog loads and the merged flush keeps both
// the historical and the new entries.
func TestPackStore_FlushMergesAfterTransientFailure(t *testing.T) {
	ctx := context.Background()
	faulty, inner := newCatalogTestStore(t)
	seedPackedRepo(t, inner, "filemeta/old1")

	faulty.failsLeft = 1
	ps := newPackStoreT(t, faulty)
	if err := ps.Put(ctx, "filemeta/new", []byte("new run")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := ps.Flush(ctx); err == nil {
		t.Fatal("first Flush should fail while the catalog is unreadable")
	}
	// Backend has recovered; the retry must succeed and preserve history.
	if err := ps.Flush(ctx); err != nil {
		t.Fatalf("retry Flush: %v", err)
	}

	recovered := newPackStoreT(t, inner)
	for _, key := range []string{"filemeta/old1", "filemeta/new"} {
		if _, err := recovered.Get(ctx, key); err != nil {
			t.Errorf("get %s after merged flush: %v", key, err)
		}
	}
}

// A fresh repository legitimately has no catalog; that must not be an error.
func TestPackStore_FreshRepositoryFlushSucceeds(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)

	ps := newPackStoreT(t, inner)
	if err := ps.Put(ctx, "filemeta/a", []byte("first")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatalf("flush on fresh repository: %v", err)
	}

	keys, err := ps.List(ctx, "filemeta/")
	if err != nil {
		t.Fatalf("list on fresh repository: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 key, got %v", keys)
	}
}
