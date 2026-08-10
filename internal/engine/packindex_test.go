package engine

import (
	"context"
	"fmt"

	"testing"

	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
)

// backupOnce runs one backup against backend the way a separate CLI invocation
// would: over a freshly built PackStore, which has absorbed nothing and must
// read the stored index from scratch.
func backupOnce(t *testing.T, backend *MockStore, run int) {
	t.Helper()

	packed, err := storelayer.NewPackStore(backend)
	if err != nil {
		t.Fatalf("NewPackStore: %v", err)
	}

	src := NewMockSource()
	src.AddFile(fmt.Sprintf("file%d.txt", run), fmt.Sprintf("id%d", run), fmt.Appendf(nil, "contents %d", run))

	mgr := NewBackupManager(Deps{Store: packed, Reporter: ui.NewNoOpReporter()}, src)
	if _, err := mgr.Run(context.Background()); err != nil {
		t.Fatalf("backup %d: %v", run, err)
	}
}

func indexObjects(t *testing.T, backend *MockStore) []string {
	t.Helper()
	keys, err := backend.List(context.Background(), "index/packmap/")
	if err != nil {
		t.Fatalf("list shards: %v", err)
	}
	return keys
}

// The pack index is append-only: every flush writes its own shard, and every
// operation that opens the repository afterwards reads all of them, one request
// each. Left alone that grows with the number of backups a repository has ever
// taken — 80 backups cost `check`, `ls` and `find` 81 index requests each
// before any of them did work of their own — and only `prune` ever bounded it,
// which a repository that is only backed up never runs.
//
// So the bound asserted here is the point: not that compaction happened, but
// that the number of objects the *next* operation has to read stays a constant
// no matter how long the repository has been in use.
func TestBackupManager_Run_BoundsPackIndexGrowth(t *testing.T) {
	backend := NewMockStore()

	const backups = packIndexCompactThreshold * 3
	for i := 1; i <= backups; i++ {
		backupOnce(t, backend, i)

		shards := indexObjects(t, backend)
		// Crossing the threshold is what triggers the consolidation, so one
		// backup may observe threshold+1 before doing it.
		if len(shards) > packIndexCompactThreshold+1 {
			t.Fatalf("after %d backups the repository has %d pack index objects, want at most %d",
				i, len(shards), packIndexCompactThreshold+1)
		}
	}

	// Without consolidation there would be one shard per backup, so the bound
	// above has to be shown to be a bound and not merely a repository that
	// writes few shards.
	if got := len(indexObjects(t, backend)); got >= backups {
		t.Fatalf("pack index holds %d objects after %d backups; the shards are not being consolidated", got, backups)
	}
}

// A repository below the threshold must not pay for consolidation it does not
// need: the exclusive lock, the rewrite and the deletes all cost requests, and
// spending them on every backup would be a worse trade than the reads they
// save.
func TestBackupManager_Run_DoesNotCompactBelowThreshold(t *testing.T) {
	backend := NewMockStore()

	for i := 1; i <= packIndexCompactThreshold; i++ {
		backupOnce(t, backend, i)
	}

	if got := len(indexObjects(t, backend)); got != packIndexCompactThreshold {
		t.Fatalf("pack index holds %d objects after %d backups, want one per backup — compaction ran early",
			got, packIndexCompactThreshold)
	}
}

// Compaction is an optimisation running after the backup has already succeeded,
// so nothing it can fail at may fail the backup. Another process holding a lock
// is the ordinary case: concurrent backups all skip it, and whichever finishes
// alone does the work.
func TestBackupManager_Run_SucceedsWhenCompactionIsBlocked(t *testing.T) {
	ctx := context.Background()
	backend := NewMockStore()

	for i := 1; i <= packIndexCompactThreshold+1; i++ {
		backupOnce(t, backend, i)
	}
	before := len(indexObjects(t, backend))

	// Stand in for a concurrent operation by holding a shared lock across the
	// next backup, which blocks the exclusive lock compaction needs.
	held, _, err := AcquireSharedLock(ctx, backend, "concurrent reader")
	if err != nil {
		t.Fatalf("acquire shared lock: %v", err)
	}
	defer held.Release()

	backupOnce(t, backend, 99)

	if got := len(indexObjects(t, backend)); got <= before {
		t.Fatalf("pack index went from %d to %d objects while another lock was held: compaction ran anyway", before, got)
	}
}

// findPackStore has to answer "does this repository pack at all", so it must
// reach through the wrappers a real client layers on top and report nil rather
// than a zero value when there is no PackStore to find.
func TestFindPackStore(t *testing.T) {
	backend := NewMockStore()
	packed, err := storelayer.NewPackStore(backend)
	if err != nil {
		t.Fatalf("NewPackStore: %v", err)
	}

	if got := findPackStore(storelayer.NewKeyCacheStore(storelayer.NewMeteredStore(packed))); got != packed {
		t.Errorf("findPackStore through a wrapped chain = %v, want the PackStore", got)
	}
	if got := findPackStore(storelayer.NewKeyCacheStore(backend)); got != nil {
		t.Errorf("findPackStore with packing disabled = %v, want nil", got)
	}
}

// A shard is what the next operation pays a request for, so the count has to be
// of stored index objects this store read — not of catalog entries, which
// consolidation does not change.
func TestPackStoreIndexObjectCount(t *testing.T) {
	ctx := context.Background()
	backend := NewMockStore()

	for i := range 3 {
		packed, err := storelayer.NewPackStore(backend)
		if err != nil {
			t.Fatalf("NewPackStore: %v", err)
		}
		if err := packed.Put(ctx, fmt.Sprintf("chunk/%064x", i), []byte("payload")); err != nil {
			t.Fatalf("put: %v", err)
		}
		if err := packed.Flush(ctx); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}

	fresh, err := storelayer.NewPackStore(backend)
	if err != nil {
		t.Fatalf("NewPackStore: %v", err)
	}
	if got := fresh.IndexObjectCount(); got != 0 {
		t.Errorf("IndexObjectCount before the catalog is loaded = %d, want 0", got)
	}
	if _, err := fresh.Get(ctx, fmt.Sprintf("chunk/%064x", 0)); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got, want := fresh.IndexObjectCount(), len(indexObjects(t, backend)); got != want {
		t.Errorf("IndexObjectCount = %d, want %d — one per stored index object", got, want)
	}
}
