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

	checkPath := func(parentID, fileID, expectedPath string) {
		t.Helper()
		ref, err := tree.Lookup(result.Root, parentID, fileID)
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

	checkPath("", "FOLDER_A", "Documents")
	checkPath("FOLDER_A", "FOLDER_B", "Documents/Photos")
	checkPath("FOLDER_B", "FILE_C", "Documents/Photos/pic.jpg")
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
		ref, err := tree.Lookup(root, "", key)
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
	} else if meta1.Name != "file1.txt" {
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
	} else if meta1v2.ContentHash != meta1.ContentHash {
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

// TestFindPreviousSnapshot_Identity verifies that findPreviousSnapshot
// uses Identity for matching when present, enabling cross-machine
// incremental backup for portable drives.
func TestFindPreviousSnapshot_Identity(t *testing.T) {
	s := NewMockStore()

	// Create snapshots from two different machines backing up the same drive.
	macSnap := &core.Snapshot{
		Seq:     1,
		Created: "2026-03-01T10:00:00Z",
		Root:    "node/mac",
		Source: &core.SourceInfo{
			Type:     "local",
			Account:  "mac-studio.local",
			Path:     ".",
			Identity: "A1B2C3D4-1234-5678-ABCD-EF0123456789",
			PathID:   ".",
		},
	}
	linuxSnap := &core.Snapshot{
		Seq:     2,
		Created: "2026-03-02T10:00:00Z",
		Root:    "node/linux",
		Source: &core.SourceInfo{
			Type:     "local",
			Account:  "linux-workstation",
			Path:     ".",
			Identity: "A1B2C3D4-1234-5678-ABCD-EF0123456789",
			PathID:   ".",
		},
	}

	ref1 := putSnapshot(t, s, macSnap)
	ref2 := putSnapshot(t, s, linuxSnap)

	// Build catalog with newest first.
	putCatalog(t, s, []core.SnapshotSummary{
		{Ref: ref2, Created: linuxSnap.Created, Root: linuxSnap.Root, Source: linuxSnap.Source},
		{Ref: ref1, Created: macSnap.Created, Root: macSnap.Root, Source: macSnap.Source},
	})

	src := NewMockSource()
	bm := NewBackupManager(src, s, ui.NewNoOpReporter(), nil)

	// Search from the Mac with same identity and same selected-root path.
	info := core.SourceInfo{
		Type:     "local",
		Account:  "mac-studio.local",
		Path:     ".",
		Identity: "A1B2C3D4-1234-5678-ABCD-EF0123456789",
		PathID:   ".",
	}
	prev := bm.findPreviousSnapshot(info)
	if prev == nil {
		t.Fatal("expected to find previous snapshot via identity match")
	} else if prev.Root != "node/linux" {
		// Should return the most recent (Linux) snapshot since catalog is newest-first.
		t.Errorf("expected linux snapshot (newest), got root=%s", prev.Root)
	}
}

// TestFindPreviousSnapshot_LegacyFallback verifies that snapshots without
// Identity are still found by the traditional account+path match.
func TestFindPreviousSnapshot_LegacyFallback(t *testing.T) {
	s := NewMockStore()

	snap := &core.Snapshot{
		Seq:     1,
		Created: "2026-03-01T10:00:00Z",
		Root:    "node/legacy",
		Source: &core.SourceInfo{
			Type:    "local",
			Account: "myhost",
			Path:    "/data",
		},
	}

	ref := putSnapshot(t, s, snap)
	putCatalog(t, s, []core.SnapshotSummary{
		{Ref: ref, Created: snap.Created, Root: snap.Root, Source: snap.Source},
	})

	src := NewMockSource()
	bm := NewBackupManager(src, s, ui.NewNoOpReporter(), nil)

	// Search without VolumeUUID — should fall back to account+path.
	info := core.SourceInfo{
		Type:    "local",
		Account: "myhost",
		Path:    "/data",
	}
	prev := bm.findPreviousSnapshot(info)
	if prev == nil {
		t.Fatal("expected to find previous snapshot via legacy match")
	} else if prev.Root != "node/legacy" {
		t.Errorf("expected root=node/legacy, got %s", prev.Root)
	}
}

// TestFindPreviousSnapshot_IdentityPreferredOverLegacy verifies that Identity
// match takes precedence when both UUID and account+path could match
// different snapshots.
func TestFindPreviousSnapshot_IdentityPreferredOverLegacy(t *testing.T) {
	s := NewMockStore()

	// Old snapshot from same machine, same path, no UUID.
	oldSnap := &core.Snapshot{
		Seq:     1,
		Created: "2026-03-01T10:00:00Z",
		Root:    "node/old",
		Source: &core.SourceInfo{
			Type:    "local",
			Account: "mac-studio.local",
			Path:    "/Volumes/MyDrive",
		},
	}
	// Newer snapshot from different machine with identity (portable path).
	newSnap := &core.Snapshot{
		Seq:     2,
		Created: "2026-03-02T10:00:00Z",
		Root:    "node/new",
		Source: &core.SourceInfo{
			Type:     "local",
			Account:  "linux-workstation",
			Path:     ".",
			Identity: "UUID-1234",
			PathID:   ".",
		},
	}

	ref1 := putSnapshot(t, s, oldSnap)
	ref2 := putSnapshot(t, s, newSnap)

	putCatalog(t, s, []core.SnapshotSummary{
		{Ref: ref2, Created: newSnap.Created, Root: newSnap.Root, Source: newSnap.Source},
		{Ref: ref1, Created: oldSnap.Created, Root: oldSnap.Root, Source: oldSnap.Source},
	})

	src := NewMockSource()
	bm := NewBackupManager(src, s, ui.NewNoOpReporter(), nil)

	// Search with identity — should find the identity-matched snapshot first.
	info := core.SourceInfo{
		Type:     "local",
		Account:  "mac-studio.local",
		Path:     ".",
		Identity: "UUID-1234",
		PathID:   ".",
	}
	prev := bm.findPreviousSnapshot(info)
	if prev == nil {
		t.Fatal("expected to find previous snapshot")
	} else if prev.Root != "node/new" {
		t.Errorf("expected identity-matched snapshot (node/new), got root=%s", prev.Root)
	}
}

// TestFindPreviousSnapshot_IdentityDifferentSubdirs verifies that backups of
// different sub-directories on the same drive do not match each other.
func TestFindPreviousSnapshot_IdentityDifferentSubdirs(t *testing.T) {
	s := NewMockStore()

	photosSnap := &core.Snapshot{
		Seq:     1,
		Created: "2026-03-01T10:00:00Z",
		Root:    "node/photos",
		Source: &core.SourceInfo{
			Type:     "local",
			Account:  "mac-studio.local",
			Path:     "Photos",
			Identity: "UUID-SAME-DRIVE",
			PathID:   "Photos",
		},
	}

	ref := putSnapshot(t, s, photosSnap)
	putCatalog(t, s, []core.SnapshotSummary{
		{Ref: ref, Created: photosSnap.Created, Root: photosSnap.Root, Source: photosSnap.Source},
	})

	src := NewMockSource()
	bm := NewBackupManager(src, s, ui.NewNoOpReporter(), nil)

	// Search for Documents on the same drive — should NOT match Photos.
	info := core.SourceInfo{
		Type:     "local",
		Account:  "mac-studio.local",
		Path:     "Documents",
		Identity: "UUID-SAME-DRIVE",
		PathID:   "Documents",
	}
	prev := bm.findPreviousSnapshot(info)
	if prev != nil {
		t.Errorf("expected nil (different subdir on same drive), got root=%s", prev.Root)
	}
}
