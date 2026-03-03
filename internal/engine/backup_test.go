package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// TestBackupManager_ResolvesPathsForOpaqueIDs verifies that when a source
// emits FileMeta without Paths (e.g. incremental/changes sources with opaque
// cloud IDs), the backup engine resolves Paths by walking the HAMT parent chain.
func TestBackupManager_ResolvesPathsForOpaqueIDs(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()
	dest := NewMockStore()

	// Simulate a cloud drive tree with opaque IDs and NO Paths set.
	// The backup engine should resolve them.
	//   Documents/           (FOLDER_A)
	//   Documents/Photos/    (FOLDER_B)
	//   Documents/Photos/pic.jpg  (FILE_C)
	src.Files["FOLDER_A"] = MockFile{
		Meta: core.FileMeta{
			FileID: "FOLDER_A",
			Name:   "Documents",
			Type:   core.FileTypeFolder,
			Extra:  map[string]interface{}{"mimeType": "folder"},
		},
	}
	src.Files["FOLDER_B"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "FOLDER_B",
			Name:    "Photos",
			Type:    core.FileTypeFolder,
			Parents: []string{"FOLDER_A"},
			Extra:   map[string]interface{}{"mimeType": "folder"},
		},
	}
	src.Files["FILE_C"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "FILE_C",
			Name:    "pic.jpg",
			Parents: []string{"FOLDER_B"},
		},
		Content: []byte("jpeg"),
	}

	mgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	result, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	// Read back the stored FileMeta and verify Paths were resolved.
	readStore := store.NewCompressedStore(dest)
	tree := hamt.NewTree(readStore)

	checkPath := func(fileID, expectedPath string) {
		t.Helper()
		ref, err := tree.Lookup(result.Root, fileID)
		if err != nil || ref == "" {
			t.Fatalf("Lookup %s: ref=%q err=%v", fileID, ref, err)
		}
		data, err := readStore.Get(ctx, ref)
		if err != nil {
			t.Fatalf("Get %s: %v", ref, err)
		}
		var fm core.FileMeta
		if err := json.Unmarshal(data, &fm); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if len(fm.Paths) == 0 {
			t.Errorf("%s: Paths is empty, expected %q", fileID, expectedPath)
		} else if fm.Paths[0] != expectedPath {
			t.Errorf("%s: Paths[0]=%q, expected %q", fileID, fm.Paths[0], expectedPath)
		}
	}

	checkPath("FOLDER_A", "Documents")
	checkPath("FOLDER_B", "Documents/Photos")
	checkPath("FILE_C", "Documents/Photos/pic.jpg")
}

func TestBackupManager_Run(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()
	dest := NewMockStore()
	reporter := ui.NewNoOpReporter()

	src.AddFile("file1.txt", "id1", []byte("hello world"))
	src.AddFile("file2.txt", "id2", []byte("another file"))

	mgr := NewBackupManager(src, dest, reporter, nil, WithVerbose())

	// Read store wraps dest with CompressedStore so we can read back
	// the compressed data written by the BackupManager.
	readStore := store.NewCompressedStore(dest)

	// Helper: lookup a key in the HAMT and load the FileMeta from the store.
	lookupMeta := func(root, key string) *core.FileMeta {
		t.Helper()
		tree := hamt.NewTree(readStore)
		ref, err := tree.Lookup(root, key)
		if err != nil {
			t.Fatalf("Lookup %s: %v", key, err)
		}
		if ref == "" {
			return nil
		}
		data, err := readStore.Get(ctx, ref)
		if err != nil {
			t.Fatalf("Get %s: %v", ref, err)
		}
		var fm core.FileMeta
		if err := json.Unmarshal(data, &fm); err != nil {
			t.Fatalf("Unmarshal filemeta: %v", err)
		}
		return &fm
	}

	// 1. Run Initial Backup
	if _, err := mgr.Run(ctx); err != nil {
		t.Fatalf("First backup failed: %v", err)
	}

	idxData, err := readStore.Get(ctx, "index/latest")
	if err != nil {
		t.Fatal("index/latest not found")
	}
	var idx core.Index
	if err := json.Unmarshal(idxData, &idx); err != nil {
		t.Fatalf("Unmarshal index: %v", err)
	}
	if idx.Seq != 1 {
		t.Errorf("Expected Seq 1, got %d", idx.Seq)
	}

	snapData, err := readStore.Get(ctx, idx.LatestSnapshot)
	if err != nil {
		t.Fatal("Snapshot not found")
	}
	var snap core.Snapshot
	if err := json.Unmarshal(snapData, &snap); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}

	meta1 := lookupMeta(snap.Root, "id1")
	if meta1 == nil {
		t.Fatal("id1 not found in backup")
	}
	if meta1.Name != "file1.txt" {
		t.Errorf("Name mismatch: %s", meta1.Name)
	}

	// 2. Modify file2 and Run Backup again
	src.AddFile("file2.txt", "id2", []byte("modified content"))

	if _, err := mgr.Run(ctx); err != nil {
		t.Fatalf("Second backup failed: %v", err)
	}

	idxData2, _ := readStore.Get(ctx, "index/latest")
	var idx2 core.Index
	if err := json.Unmarshal(idxData2, &idx2); err != nil {
		t.Fatalf("Unmarshal index: %v", err)
	}
	if idx2.Seq != 2 {
		t.Errorf("Expected Seq 2, got %d", idx2.Seq)
	}

	snapData2, _ := readStore.Get(ctx, idx2.LatestSnapshot)
	var snap2 core.Snapshot
	if err := json.Unmarshal(snapData2, &snap2); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}

	meta2v2 := lookupMeta(snap2.Root, "id2")
	if meta2v2.Size != int64(len("modified content")) {
		t.Errorf("Size mismatch for modified file. Expected %d, got %d", len("modified content"), meta2v2.Size)
	}

	meta1v2 := lookupMeta(snap2.Root, "id1")
	if meta1v2 == nil {
		t.Fatal("id1 missing in second snapshot")
	}

	if meta1v2.ContentHash != meta1.ContentHash {
		t.Errorf("ContentHash changed for unchanged file")
	}

	// 3. Delete file1 and Run Backup again
	delete(src.Files, "id1")

	if _, err := mgr.Run(ctx); err != nil {
		t.Fatalf("Third backup failed: %v", err)
	}

	idxData3, _ := readStore.Get(ctx, "index/latest")
	var idx3 core.Index
	if err := json.Unmarshal(idxData3, &idx3); err != nil {
		t.Fatalf("Unmarshal index: %v", err)
	}
	if idx3.Seq != 3 {
		t.Errorf("Expected Seq 3, got %d", idx3.Seq)
	}

	snapData3, _ := readStore.Get(ctx, idx3.LatestSnapshot)
	var snap3 core.Snapshot
	if err := json.Unmarshal(snapData3, &snap3); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}

	meta1v3 := lookupMeta(snap3.Root, "id1")
	if meta1v3 != nil {
		t.Error("id1 should be deleted in third snapshot, but found")
	}

	meta2v3 := lookupMeta(snap3.Root, "id2")
	if meta2v3 == nil {
		t.Error("id2 missing in third snapshot")
	}
}
