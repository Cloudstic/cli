package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
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

// The reachable set holds keys in a compact form that only "<namespace><64
// lowercase hex>" fits. Every key that does not fit must survive it anyway.
//
// docs/compatibility.md is normative: a garbage collector must never read
// "cannot represent" as "not referenced". A key dropped on the way into the
// reachable set is an object the sweep then lists, fails to find marked, and
// deletes — a live object destroyed by a representation choice. So this walks a
// real repository whose chunk refs take shapes the compact form refuses, and
// requires them to be there afterwards.
func TestPruneManager_KeepsObjectsWhoseKeysDoNotFitTheCompactForm(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockStore()

	// One key of each shape the compact encoding accepts and refuses: a
	// canonical digest, a short legacy name, and uppercase hex — which decodes
	// under a case-insensitive hex decoder and would then be filed under the
	// same entry as its lowercase twin.
	chunkRefs := []string{
		"chunk/" + strings.Repeat("ab", 32),
		"chunk/legacy-chunk-name",
		"chunk/" + strings.ToUpper(strings.Repeat("cd", 32)),
	}
	for _, ref := range chunkRefs {
		if err := mockStore.Put(ctx, ref, []byte("data")); err != nil {
			t.Fatalf("put %s: %v", ref, err)
		}
	}

	content := core.Content{Chunks: chunkRefs}
	_, contentData, _ := core.ComputeJSONHash(&content)
	contentRef := "content/legacy-content-name"
	if err := mockStore.Put(ctx, contentRef, contentData); err != nil {
		t.Fatalf("put content: %v", err)
	}

	meta := core.FileMeta{ContentHash: "legacy-content-name", Name: "legacy.txt"}
	metaHash, metaData, _ := core.ComputeJSONHash(&meta)
	metaRef := "filemeta/" + metaHash
	if err := mockStore.Put(ctx, metaRef, metaData); err != nil {
		t.Fatalf("put filemeta: %v", err)
	}

	rootRef, err := insertCommit(ctx, hamt.NewTree(mockStore), "", "", "file1", metaRef)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	snap := core.Snapshot{Root: rootRef, Seq: 1}
	snapHash, snapData, _ := core.ComputeJSONHash(&snap)
	if err := mockStore.Put(ctx, "snapshot/"+snapHash, snapData); err != nil {
		t.Fatalf("put snapshot: %v", err)
	}

	// Something must actually be collectable, or a prune that marked everything
	// by accident would pass this test.
	if err := mockStore.Put(ctx, "chunk/garbage", []byte("trash")); err != nil {
		t.Fatalf("put garbage: %v", err)
	}

	pm := NewPruneManager(Deps{
		Store:    storelayer.NewMeteredStore(mockStore),
		Reporter: ui.NewNoOpReporter(),
	})
	if _, err := pm.Run(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}

	for _, ref := range chunkRefs {
		assertExists(t, ctx, mockStore, ref)
	}
	assertExists(t, ctx, mockStore, contentRef)
	assertExists(t, ctx, mockStore, metaRef)
	assertExists(t, ctx, mockStore, rootRef)
	assertNotExists(t, ctx, mockStore, "chunk/garbage")
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
