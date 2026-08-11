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

// hiddenSnapshotStore reports no snapshots while the rest of the repository is
// intact, reproducing what an unreadable index looks like to the mark phase.
type hiddenSnapshotStore struct {
	store.ObjectStore
}

func (h *hiddenSnapshotStore) List(ctx context.Context, prefix string) ([]string, error) {
	if strings.HasPrefix(prefix, "snapshot/") {
		return nil, nil
	}
	return h.ObjectStore.List(ctx, prefix)
}

// Prune must never interpret "no snapshots" as "everything is garbage" while
// the repository still holds objects.
func TestPruneManager_AbortsWhenSnapshotsVanishButObjectsRemain(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()

	if err := mockStore.Put(ctx, "chunk/live", []byte("data")); err != nil {
		t.Fatalf("put chunk: %v", err)
	}
	if err := mockStore.Put(ctx, "filemeta/live", []byte("{}")); err != nil {
		t.Fatalf("put filemeta: %v", err)
	}

	metered := storelayer.NewMeteredStore(&hiddenSnapshotStore{ObjectStore: mockStore})
	pm := NewPruneManager(Deps{Store: metered, Reporter: ui.NewNoOpReporter()})

	result, err := pm.Run(ctx)
	if err == nil {
		t.Fatalf("prune succeeded and deleted %d objects; want an abort", result.ObjectsDeleted)
	}
	if !strings.Contains(err.Error(), "no snapshots found") {
		t.Errorf("error should explain the abort, got: %v", err)
	}

	assertExists(t, ctx, mockStore, "chunk/live")
	assertExists(t, ctx, mockStore, "filemeta/live")
}

// A genuinely empty repository is a legitimate state and must still prune.
func TestPruneManager_EmptyRepositorySucceeds(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()

	metered := storelayer.NewMeteredStore(mockStore)
	pm := NewPruneManager(Deps{Store: metered, Reporter: ui.NewNoOpReporter()})

	result, err := pm.Run(ctx)
	if err != nil {
		t.Fatalf("prune on an empty repository: %v", err)
	}
	if result.ObjectsDeleted != 0 {
		t.Errorf("expected 0 deletions, got %d", result.ObjectsDeleted)
	}
}

// A listing that fails during the sweep must abort, not be skipped.
//
// docs/compatibility.md is normative here: prune must not proceed on data it
// could not fully read. Skipping the prefix errs safe in the narrow sense that
// nothing is deleted from it, but prune then reports a success and an object
// count describing a repository it only partly examined, and nothing records
// that a prefix was missed.
func TestPruneManager_AbortsWhenSweepListingFails(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()

	src := NewMockSource()
	src.AddFile("keep.txt", "id1", []byte("data"))
	if _, err := NewBackupManager(Deps{Store: mockStore, Reporter: ui.NewNoOpReporter()}, src).Run(ctx); err != nil {
		t.Fatalf("seed backup: %v", err)
	}

	listErr := errors.New("simulated listing failure")
	faulty := &storetest.FaultStore{
		ObjectStore: mockStore,
		FailList: func(prefix string, _ int) error {
			if strings.HasPrefix(prefix, "chunk/") {
				return listErr
			}
			return nil
		},
	}

	pm := NewPruneManager(Deps{Store: storelayer.NewMeteredStore(faulty), Reporter: ui.NewNoOpReporter()})
	result, err := pm.Run(ctx)
	if err == nil {
		t.Fatalf("prune reported success over an unreadable listing, deleting %d objects", result.ObjectsDeleted)
	}
	if !errors.Is(err, listErr) {
		t.Errorf("error should wrap the listing failure so the cause is inspectable, got: %v", err)
	}
}
