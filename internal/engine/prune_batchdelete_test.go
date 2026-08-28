package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

// batchRecordingStore batches, records what it was asked to delete, and can
// refuse named keys — which is how a partial DeleteObjects response reaches the
// sweep without a live S3 that can be made to refuse one key of a batch.
type batchRecordingStore struct {
	*MockStore
	batches [][]string
	singles []string
	refuse  map[string]error
}

func newBatchRecordingStore() *batchRecordingStore {
	return &batchRecordingStore{MockStore: NewMockStore(), refuse: map[string]error{}}
}

func (b *batchRecordingStore) Delete(ctx context.Context, key string) error {
	b.singles = append(b.singles, key)
	if err, ok := b.refuse[key]; ok {
		return err
	}
	return b.MockStore.Delete(ctx, key)
}

func (b *batchRecordingStore) DeleteAll(ctx context.Context, keys []string) error {
	b.batches = append(b.batches, append([]string(nil), keys...))
	var failures store.DeleteErrors
	for _, key := range keys {
		if err, ok := b.refuse[key]; ok {
			failures = append(failures, store.DeleteError{Key: key, Err: err})
			continue
		}
		if err := b.MockStore.Delete(ctx, key); err != nil {
			failures = append(failures, store.DeleteError{Key: key, Err: err})
		}
	}
	if len(failures) > 0 {
		return failures
	}
	return nil
}

// seedPrunableRepo writes a snapshot reachable from index/latest plus the
// garbage keys given, and returns the manager to sweep it with.
func seedPrunableRepo(t *testing.T, backend store.ObjectStore, garbage ...string) *PruneManager {
	t.Helper()
	ctx := context.Background()

	src := NewMockSource()
	src.AddFile("keep.txt", "id1", []byte("data"))
	if _, err := NewBackupManager(Deps{Store: backend, Reporter: ui.NewNoOpReporter()}, src).Run(ctx); err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	for _, key := range garbage {
		if err := backend.Put(ctx, key, []byte("trash")); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	return NewPruneManager(Deps{
		Store:    storelayer.NewMeteredStore(backend),
		Reporter: ui.NewNoOpReporter(),
	})
}

// The sweep must hand its unreachable keys to the store in batches. On an
// S3-family backend that is one DeleteObjects request per thousand keys instead
// of one request per object, which is the whole point of the capability.
func TestPruneManager_SweepsInBatches(t *testing.T) {
	backend := newBatchRecordingStore()
	garbage := []string{"chunk/g1", "chunk/g2", "chunk/g3", "filemeta/g1", "content/g1"}
	pm := seedPrunableRepo(t, backend, garbage...)

	result, err := pm.Run(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.ObjectsDeleted != len(garbage) {
		t.Errorf("deleted %d objects, want %d", result.ObjectsDeleted, len(garbage))
	}

	// Well under sweepDeleteBatch, so the whole sweep is one call — the shape
	// that becomes one DeleteObjects request per thousand keys at scale.
	if len(backend.batches) != 1 || len(backend.batches[0]) != len(garbage) {
		t.Errorf("backend saw %v, want one batch of %d keys", backend.batches, len(garbage))
	}
	for _, key := range garbage {
		if exists, _ := backend.Exists(context.Background(), key); exists {
			t.Errorf("%s survived the sweep", key)
		}
	}
}

// A key the store refuses must fail the prune. prune reports objects deleted
// and space reclaimed, and reporting a success over garbage still sitting in
// the repository is exactly the misreport docs/compatibility.md forbids.
func TestPruneManager_PartialDeleteFailureFailsThePrune(t *testing.T) {
	ctx := context.Background()
	backend := newBatchRecordingStore()
	refused := errors.New("access denied")
	pm := seedPrunableRepo(t, backend, "chunk/g1", "chunk/g2", "chunk/g3")
	backend.refuse["chunk/g2"] = refused

	result, err := pm.Run(ctx)
	if err == nil {
		t.Fatalf("prune reported success over %d deletions with one key refused", result.ObjectsDeleted)
	}
	if !errors.Is(err, refused) {
		t.Errorf("the cause should stay inspectable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "could not be deleted") {
		t.Errorf("the error should say what went wrong, got: %v", err)
	}

	// The keys it could delete are still gone — one object it cannot remove is
	// no reason to leave the rest of the garbage behind.
	for _, key := range []string{"chunk/g1", "chunk/g3"} {
		if exists, _ := backend.Exists(ctx, key); exists {
			t.Errorf("%s should have been deleted anyway", key)
		}
	}
	if exists, _ := backend.Exists(ctx, "chunk/g2"); !exists {
		t.Error("the refused key should still be there")
	}
}

// A backend with no batch capability must keep working unchanged, one delete
// per key, with the same outcome.
func TestPruneManager_SweepsPerKeyWithoutTheCapability(t *testing.T) {
	ctx := context.Background()
	backend := NewMockStore()
	garbage := []string{"chunk/g1", "chunk/g2"}
	pm := seedPrunableRepo(t, backend, garbage...)

	result, err := pm.Run(ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.ObjectsDeleted != len(garbage) {
		t.Errorf("deleted %d objects, want %d", result.ObjectsDeleted, len(garbage))
	}
	for _, key := range garbage {
		if exists, _ := backend.Exists(ctx, key); exists {
			t.Errorf("%s survived the sweep", key)
		}
	}
}

// The fallback path reports failures per key too, so a backend with no batch
// capability fails the prune for the same reason and with the same detail.
func TestPruneManager_PerKeyDeleteFailureFailsThePrune(t *testing.T) {
	ctx := context.Background()
	backend := NewMockStore()
	refused := errors.New("access denied")

	faulty := &storetest.FaultStore{
		ObjectStore: backend,
		FailDelete:  storetest.FailKeys(refused, "chunk/g2"),
	}
	pm := seedPrunableRepo(t, faulty, "chunk/g1", "chunk/g2")

	result, err := pm.Run(ctx)
	if err == nil {
		t.Fatalf("prune reported success over %d deletions with one key refused", result.ObjectsDeleted)
	}
	if !errors.Is(err, refused) {
		t.Errorf("the cause should stay inspectable, got: %v", err)
	}
	if exists, _ := backend.Exists(ctx, "chunk/g1"); exists {
		t.Error("chunk/g1 should have been deleted anyway")
	}
	if exists, _ := backend.Exists(ctx, "chunk/g2"); !exists {
		t.Error("the refused key should still be there")
	}
}
