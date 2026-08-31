package engine

import (
	"bytes"
	"context"
	"testing"

	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// packedDeps builds the store chain a format-2 repository is opened with —
// CompressedStore over MeteredStore over PackStore — and returns it as a
// dependency set, along with the pack layer for flushing.
//
// A fresh chain per operation is the whole point. In a real repository each
// command is a new process whose PackStore starts with no catalog loaded, and
// reusing one across operations arms every lookup that would otherwise have to
// load it — which is exactly the state the bug needed to hide in.
func packedDeps(t *testing.T, backend store.ObjectStore) (Deps, *storelayer.PackStore) {
	t.Helper()

	packed, err := storelayer.NewPackStore(backend)
	if err != nil {
		t.Fatalf("NewPackStore: %v", err)
	}
	metered := storelayer.NewMeteredStore(packed)
	return Deps{
		Store:    storelayer.NewCompressedStore(metered),
		Reporter: ui.NewNoOpReporter(),
	}, packed
}

// TestPackedEngine_ForgetPruneReleasesAPackedSnapshot is the format-2
// counterpart of TestV3Engine_BackupRestoreCheckPrune's closing sequence: with
// packing on, a snapshot object under 512 KiB is bundled into a packfile and
// has no standalone object in the backend, so removing it means rewriting the
// pack catalog rather than deleting a key.
//
// Forget used to fail outright on such a repository. MeteredStore sizes an
// object before deleting it, PackStore.Size did not consult the catalog, and so
// the size lookup fell through to the backend and reported that no such object
// was stored — aborting the delete before PackStore.Delete, which handles
// packed keys correctly, was ever reached. Nothing was unlinked, and a
// following prune therefore had no garbage to collect.
func TestPackedEngine_ForgetPruneReleasesAPackedSnapshot(t *testing.T) {
	ctx := context.Background()
	backend := NewMockStore()

	src := NewMockSource()
	src.AddFile("a.txt", "id-a", []byte("alpha"))

	deps, packed := packedDeps(t, backend)
	first, err := NewBackupManager(deps, src).Run(ctx)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	if err := packed.Flush(ctx); err != nil {
		t.Fatalf("flush after first backup: %v", err)
	}

	src.AddFile("b.txt", "id-b", []byte("beta"))
	deps, packed = packedDeps(t, backend)
	second, err := NewBackupManager(deps, src).Run(ctx)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if err := packed.Flush(ctx); err != nil {
		t.Fatalf("flush after second backup: %v", err)
	}

	// The snapshot is genuinely packed: no standalone backend object by that
	// name, so a delete that reaches the backend can only fail.
	if _, ok := backend.Data[first.SnapshotRef]; ok {
		t.Fatalf("precondition failed: %s was stored standalone, so this test "+
			"would pass without the pack layer being consulted", first.SnapshotRef)
	}

	deps, _ = packedDeps(t, backend)
	if _, err := NewForgetManager(deps).Run(ctx, first.SnapshotRef); err != nil {
		t.Fatalf("forget packed snapshot %s: %v", first.SnapshotRef, err)
	}

	// The forgotten snapshot is gone from the repository's view of itself...
	deps, _ = packedDeps(t, backend)
	listRes, err := NewListManager(deps).Run(ctx)
	if err != nil {
		t.Fatalf("list after forget: %v", err)
	}
	if len(listRes.Snapshots) != 1 {
		t.Fatalf("list reports %d snapshots after forgetting one of two", len(listRes.Snapshots))
	}
	if listRes.Snapshots[0].Ref != second.SnapshotRef {
		t.Errorf("surviving snapshot is %s, want %s", listRes.Snapshots[0].Ref, second.SnapshotRef)
	}

	// ...and prune collects the garbage it unlinked.
	deps, _ = packedDeps(t, backend)
	pruneRes, err := NewPruneManager(deps).Run(ctx)
	if err != nil {
		t.Fatalf("prune after forget: %v", err)
	}
	if pruneRes.ObjectsDeleted == 0 {
		t.Error("prune after forget deleted nothing: the forgotten snapshot's " +
			"objects are still reachable, so nothing was ever unlinked")
	}

	// The surviving snapshot still restores, and the repository is intact.
	deps, _ = packedDeps(t, backend)
	var buf bytes.Buffer
	if _, err := NewRestoreManager(deps).Run(ctx, NewZipRestoreWriter(&buf), "latest"); err != nil {
		t.Fatalf("restore after forget and prune: %v", err)
	}

	deps, _ = packedDeps(t, backend)
	checkRes, err := NewCheckManager(deps).Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("check after forget and prune: %v", err)
	}
	if len(checkRes.Errors) != 0 {
		t.Fatalf("check errors after forget and prune: %v", checkRes.Errors)
	}
}
