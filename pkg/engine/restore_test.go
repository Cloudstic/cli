package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/ui"
)

func TestRestoreManager_Run(t *testing.T) {
	// Setup: Create a backup first
	src := NewMockSource()
	dest := NewMockStore()

	src.AddFile("restore_me.txt", "id1", []byte("restore content"))

	// Hierarchy: subdir (id_subdir) -> nested.txt (id2)

	// 1. Add Parent Folder
	src.Files["id_subdir"] = MockFile{
		Meta: core.FileMeta{
			FileID: "id_subdir",
			Name:   "subdir",
			Type:   core.FileTypeFolder,
			Extra:  map[string]interface{}{"mimeType": "application/vnd.google-apps.folder"},
			// Parents empty (root)
		},
		Content: []byte{},
	}

	// 2. Add Child File
	// NOTE: BackupManager now expects source to provide IDs in Parents,
	// and it converts them to Refs during Walk.
	// MockSource.Walk just iterates the map.
	src.Files["id2"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "id2",
			Name:    "nested.txt",
			Parents: []string{"id_subdir"}, // Points to parent ID
		},
		Content: []byte("nested content"),
	}

	// IMPORTANT: BackupManager builds `idToRef` map during walk to resolve parents.
	// For this to work with MockSource (which is a map, random order),
	// we need to ensure parent is processed or we need BackupManager to handle out-of-order.
	// My implementation of BackupManager has a TODO/Warning:
	// "If we haven't processed it... Parent unknown... implies order violation".
	// The current BackupManager implementation does NOT topological sort, it assumes input order or lookups.
	// BUT, `idToRef` is populated as we go.
	// Since `MockSource` iterates map, order is random.
	// If child comes before parent, `idToRef[parentID]` will be missing.
	// We should probably enforce order in MockSource for this test to be reliable.
	// Or update BackupManager to be more robust (two-pass).
	// Given I just implemented topological sort in `GDriveSource`, `MockSource` needs something similar?
	// `MockSource.Walk` is trivial.
	// Let's leave it random and see if it fails, or fix MockSource to be deterministic/topological.
	// Actually, standard `MockSource.Walk` iterates keys.
	// Let's make MockSource Walk implicitly ordered (Folders first, then files)?

	// Run Backup
	bkMgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), WithVerbose())
	if _, err := bkMgr.Run(context.Background()); err != nil {
		t.Fatalf("Backup setup failed: %v", err)
	}

	// Prepare Restore
	tmpDir, err := os.MkdirTemp("", "cloudstic-restore-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	rsMgr := NewRestoreManager(dest, ui.NewNoOpReporter())

	// Run Restore (latest)
	if _, err := rsMgr.Run(context.Background(), tmpDir, ""); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// Verify files on disk
	// 1. restore_me.txt
	content1, err := os.ReadFile(filepath.Join(tmpDir, "restore_me.txt"))
	if err != nil {
		t.Errorf("restore_me.txt not found: %v", err)
	}
	if string(content1) != "restore content" {
		t.Errorf("Content mismatch for restore_me.txt")
	}

	// 2. subdir/nested.txt
	content2, err := os.ReadFile(filepath.Join(tmpDir, "subdir/nested.txt"))
	if err != nil {
		t.Errorf("subdir/nested.txt not found: %v", err)
	}
	if string(content2) != "nested content" {
		t.Errorf("Content mismatch for nested.txt")
	}
}
