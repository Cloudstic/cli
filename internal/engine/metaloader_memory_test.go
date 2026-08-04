package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
)

// The two operations that walk a whole tree retain very different amounts of
// it, and which is which is a deliberate choice rather than an accident. These
// tests pin the choice, because both regressions are silent: a cache that stops
// being consulted still works, and one that starts retaining everything still
// returns the right answer.

// prune reads each filemeta at most once — markFileMeta checks the reachable
// set before loading — so a memoizing loader could never return a hit and would
// cost a core.FileMeta per object in the repository. This fails if one is
// reintroduced.
func TestPruneLoaderDoesNotMemoize(t *testing.T) {
	pm := NewPruneManager(Deps{Store: NewMockStore(), Reporter: ui.NewNoOpReporter()})
	if pm.metas.cache != nil {
		t.Error("prune's filemeta loader memoizes; its reachable set already " +
			"guarantees one load per ref, so the cache can only cost memory")
	}
}

// diff's parent lookup is consulted only as folderByID[parentID], and parents are
// folders. Retaining files there cost a core.FileMeta each for entries nothing
// read, which on a large tree was most of the map.
func TestDiffParentLookupHoldsOnlyFolders(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	folder := createMetaWithID(ctx, s, core.FileMeta{
		Version: 1, FileID: "dir", Name: "Documents", Type: core.FileTypeFolder,
	})
	fileA := createMetaWithID(ctx, s, core.FileMeta{
		Version: 1, FileID: "a", Name: "a.txt", Parents: []string{"dir"}, ContentHash: "aa",
	})
	fileB := createMetaWithID(ctx, s, core.FileMeta{
		Version: 1, FileID: "b", Name: "b.txt", Parents: []string{"dir"}, ContentHash: "bb",
	})
	root := createHamt(ctx, t, s, []string{"dir", "a", "b"}, []string{folder, fileA, fileB})

	dm := NewDiffManager(Deps{Store: s})
	folderByID, err := dm.collectFolders(ctx, root)
	if err != nil {
		t.Fatalf("collectFolders: %v", err)
	}

	if len(folderByID) != 1 {
		t.Errorf("parent lookup holds %d entries, want 1 (the folder); it retains "+
			"entries no caller reads", len(folderByID))
	}
	if _, ok := folderByID["dir"]; !ok {
		t.Error("parent lookup is missing the folder every file resolves through")
	}
}

// Filtering the parent lookup must not change any reported path — that is the
// whole point of the entries dropped being unread.
func TestDiffReportsFullPathsAfterFiltering(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	outer := createMetaWithID(ctx, s, core.FileMeta{
		Version: 1, FileID: "outer", Name: "Documents", Type: core.FileTypeFolder,
	})
	inner := createMetaWithID(ctx, s, core.FileMeta{
		Version: 1, FileID: "inner", Name: "Photos", Type: core.FileTypeFolder,
		Parents: []string{"outer"},
	})
	before := createHamt(ctx, t, s, []string{"outer", "inner"}, []string{outer, inner})

	pic := createMetaWithID(ctx, s, core.FileMeta{
		Version: 1, FileID: "pic", Name: "pic.jpg", Parents: []string{"inner"}, ContentHash: "cc",
	})
	after := createHamt(ctx, t, s,
		[]string{"outer", "inner", "pic"}, []string{outer, inner, pic})

	r1 := saveSnapshot(ctx, s, &core.Snapshot{Seq: 1, Root: before, Created: "2025-01-01T00:00:00Z"})
	r2 := saveSnapshot(ctx, s, &core.Snapshot{Seq: 2, Root: after, Created: "2025-01-02T00:00:00Z"})

	res, err := NewDiffManager(Deps{Store: s}).Run(ctx, r1, r2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(res.Changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(res.Changes))
	}
	if got := res.Changes[0].Path; got != "Documents/Photos/pic.jpg" {
		t.Errorf("Path = %q, want Documents/Photos/pic.jpg — the nested path is "+
			"resolved through the parent lookup, so filtering it must not shorten it", got)
	}
}

// TestDiffRetentionGrowsWithFoldersNotFiles is the bounded-footprint claim
// stated as a test: adding files to a tree must not grow what diff retains,
// because only folders are kept. Entry counts are used rather than heap
// measurements, which are too noisy to assert on.
func TestDiffRetentionGrowsWithFoldersNotFiles(t *testing.T) {
	build := func(files int) int {
		ctx := context.Background()
		s := NewMockStore()

		ids := []string{"dir"}
		refs := []string{createMetaWithID(ctx, s, core.FileMeta{
			Version: 1, FileID: "dir", Name: "Documents", Type: core.FileTypeFolder,
		})}
		for i := range files {
			id := fmt.Sprintf("f%d", i)
			ids = append(ids, id)
			refs = append(refs, createMetaWithID(ctx, s, core.FileMeta{
				Version: 1, FileID: id, Name: id + ".txt",
				Parents: []string{"dir"}, ContentHash: fmt.Sprintf("%02x", i),
			}))
		}
		root := createHamt(ctx, t, s, ids, refs)

		folderByID, err := NewDiffManager(Deps{Store: s}).collectFolders(ctx, root)
		if err != nil {
			t.Fatalf("collectFolders: %v", err)
		}
		return len(folderByID)
	}

	small, large := build(10), build(400)
	if small != large {
		t.Errorf("parent lookup grew from %d to %d entries when only the file count "+
			"changed; retention must track folders, not files", small, large)
	}
}
