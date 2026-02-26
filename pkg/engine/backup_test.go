package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/hamt"
	"github.com/cloudstic/cli/pkg/ui"
)

func TestBackupManager_Run(t *testing.T) {
	src := NewMockSource()
	dest := NewMockStore()
	reporter := ui.NewNoOpReporter()

	src.AddFile("file1.txt", "id1", []byte("hello world"))
	src.AddFile("file2.txt", "id2", []byte("another file"))

	mgr := NewBackupManager(src, dest, reporter, WithVerbose())

	// Helper: lookup a key in the HAMT and load the FileMeta from the store.
	lookupMeta := func(root, key string) *core.FileMeta {
		t.Helper()
		tree := hamt.NewTree(dest)
		ref, err := tree.Lookup(root, key)
		if err != nil {
			t.Fatalf("Lookup %s: %v", key, err)
		}
		if ref == "" {
			return nil
		}
		data, err := dest.Get(ref)
		if err != nil {
			t.Fatalf("Get %s: %v", ref, err)
		}
		var fm core.FileMeta
		json.Unmarshal(data, &fm)
		return &fm
	}

	// 1. Run Initial Backup
	if _, err := mgr.Run(context.Background()); err != nil {
		t.Fatalf("First backup failed: %v", err)
	}

	idxData, err := dest.Get("index/latest")
	if err != nil {
		t.Fatal("index/latest not found")
	}
	var idx core.Index
	json.Unmarshal(idxData, &idx)
	if idx.Seq != 1 {
		t.Errorf("Expected Seq 1, got %d", idx.Seq)
	}

	snapData, err := dest.Get(idx.LatestSnapshot)
	if err != nil {
		t.Fatal("Snapshot not found")
	}
	var snap core.Snapshot
	json.Unmarshal(snapData, &snap)

	meta1 := lookupMeta(snap.Root, "id1")
	if meta1 == nil {
		t.Fatal("id1 not found in backup")
	}
	if meta1.Name != "file1.txt" {
		t.Errorf("Name mismatch: %s", meta1.Name)
	}

	// 2. Modify file2 and Run Backup again
	src.AddFile("file2.txt", "id2", []byte("modified content"))

	if _, err := mgr.Run(context.Background()); err != nil {
		t.Fatalf("Second backup failed: %v", err)
	}

	idxData2, _ := dest.Get("index/latest")
	var idx2 core.Index
	json.Unmarshal(idxData2, &idx2)
	if idx2.Seq != 2 {
		t.Errorf("Expected Seq 2, got %d", idx2.Seq)
	}

	snapData2, _ := dest.Get(idx2.LatestSnapshot)
	var snap2 core.Snapshot
	json.Unmarshal(snapData2, &snap2)

	meta2v2 := lookupMeta(snap2.Root, "id2")
	if meta2v2.Size != int64(len("modified content")) {
		t.Errorf("Size mismatch for modified file. Expected %d, got %d", len("modified content"), meta2v2.Size)
	}

	meta1v2 := lookupMeta(snap2.Root, "id1")
	if meta1v2 == nil {
		t.Error("id1 missing in second snapshot")
	}

	if meta1v2.ContentHash != meta1.ContentHash {
		t.Errorf("ContentHash changed for unchanged file")
	}

	// 3. Delete file1 and Run Backup again
	delete(src.Files, "id1")

	if _, err := mgr.Run(context.Background()); err != nil {
		t.Fatalf("Third backup failed: %v", err)
	}

	idxData3, _ := dest.Get("index/latest")
	var idx3 core.Index
	json.Unmarshal(idxData3, &idx3)
	if idx3.Seq != 3 {
		t.Errorf("Expected Seq 3, got %d", idx3.Seq)
	}

	snapData3, _ := dest.Get(idx3.LatestSnapshot)
	var snap3 core.Snapshot
	json.Unmarshal(snapData3, &snap3)

	meta1v3 := lookupMeta(snap3.Root, "id1")
	if meta1v3 != nil {
		t.Error("id1 should be deleted in third snapshot, but found")
	}

	meta2v3 := lookupMeta(snap3.Root, "id2")
	if meta2v3 == nil {
		t.Error("id2 missing in third snapshot")
	}
}
