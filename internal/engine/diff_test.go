package engine

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
)

func TestDiffManager_Run(t *testing.T) {
	ctx := context.Background()
	store := NewMockStore()

	meta1 := createMeta(ctx, store, "file1.txt", 100)
	meta2 := createMeta(ctx, store, "file2.txt", 200)
	meta3 := createMeta(ctx, store, "file3.txt", 300)

	root1 := createHamt(t, store, []string{"file1", "file2"}, []string{meta1, meta2})
	_ = saveSnapshotRef(ctx, store, root1, 1)

	meta2Mod := createMeta(ctx, store, "file2.txt", 250)
	root2 := createHamt(t, store, []string{"file1", "file2", "file3"}, []string{meta1, meta2Mod, meta3})
	snap2Ref := saveSnapshotRef(ctx, store, root2, 2)
	_ = store.Put(ctx, "index/latest", createIndex(snap2Ref, 2))

	dm := NewDiffManager(store)

	changes, err := dm.diffRoots(root1, root2)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}

	if len(changes) != 2 {
		t.Errorf("Expected 2 changes, got %d", len(changes))
	}

	m := make(map[string]ChangeType)
	for _, c := range changes {
		m[c.Path] = c.Type
	}

	if m["file2.txt"] != ChangeModified {
		t.Errorf("Expected file2.txt to be Modified, got %v", m["file2.txt"])
	}
	if m["file3.txt"] != ChangeAdded {
		t.Errorf("Expected file3.txt to be Added, got %v", m["file3.txt"])
	}

	changesRev, err := dm.diffRoots(root2, root1)
	if err != nil {
		t.Fatalf("Diff reverse failed: %v", err)
	}

	mRev := make(map[string]ChangeType)
	for _, c := range changesRev {
		mRev[c.Path] = c.Type
	}

	if mRev["file2.txt"] != ChangeModified {
		t.Errorf("Expected file2.txt to be Modified (reverse), got %v", mRev["file2.txt"])
	}
	if mRev["file3.txt"] != ChangeRemoved {
		t.Errorf("Expected file3.txt to be Removed, got %v", mRev["file3.txt"])
	}
}

func TestDiffManager_DerivesPathsFromParentsWithoutPersistedPaths(t *testing.T) {
	ctx := context.Background()
	store := NewMockStore()

	rootDir := createMetaWithID(ctx, store, core.FileMeta{
		FileID: "dir",
		Name:   "docs",
		Type:   core.FileTypeFolder,
	})
	oldFile := createMetaWithID(ctx, store, core.FileMeta{
		FileID:  "file",
		Name:    "guide.txt",
		Type:    core.FileTypeFile,
		Parents: []string{"dir"},
		Size:    100,
		Paths:   []string{"docs/guide.txt"},
	})
	newFile := createMetaWithID(ctx, store, core.FileMeta{
		FileID:  "file",
		Name:    "guide.txt",
		Type:    core.FileTypeFile,
		Parents: []string{"dir"},
		Size:    200,
	})

	root1 := createHamt(t, store, []string{"dir", "file"}, []string{rootDir, oldFile})
	root2 := createHamt(t, store, []string{"dir", "file"}, []string{rootDir, newFile})

	dm := NewDiffManager(store)
	changes, err := dm.diffRoots(root1, root2)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(changes))
	}
	if changes[0].Path != "docs/guide.txt" {
		t.Fatalf("Expected derived path docs/guide.txt, got %q", changes[0].Path)
	}
}

func createMeta(ctx context.Context, s *MockStore, name string, size int64) string {
	m := core.FileMeta{Name: name, Size: size}
	h, d, _ := core.ComputeJSONHash(&m)
	ref := "filemeta/" + h
	_ = s.Put(ctx, ref, d)
	return ref
}

func createMetaWithID(ctx context.Context, s *MockStore, m core.FileMeta) string {
	h, d, _ := core.ComputeJSONHash(&m)
	ref := "filemeta/" + h
	_ = s.Put(ctx, ref, d)
	return ref
}

func createHamt(t *testing.T, s *MockStore, ids []string, refs []string) string {
	tree := hamt.NewTree(s)
	root := ""
	for i, id := range ids {
		var err error
		root, err = tree.Insert(root, "", id, refs[i])
		if err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}
	return root
}

func saveSnapshotRef(ctx context.Context, s *MockStore, root string, seq int) string {
	snap := core.Snapshot{Root: root, Seq: seq}
	h, d, _ := core.ComputeJSONHash(&snap)
	ref := "snapshot/" + h
	_ = s.Put(ctx, ref, d)
	return ref
}
