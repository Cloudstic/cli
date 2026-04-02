package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// setupBackupForRestore creates a backup with a known file tree for restore tests:
//
//	restore_me.txt       -> "restore content"
//	subdir/              -> (folder)
//	subdir/nested.txt    -> "nested content"
//	subdir/deep/         -> (folder)
//	subdir/deep/file.txt -> "deep content"
func setupBackupForRestore(t *testing.T) *MockStore {
	t.Helper()
	src := NewMockSource()
	dest := NewMockStore()

	src.AddFile("restore_me.txt", "id1", []byte("restore content"))

	src.Files["id_subdir"] = MockFile{
		Meta: core.FileMeta{
			FileID: "id_subdir",
			Name:   "subdir",
			Type:   core.FileTypeFolder,
			Extra:  map[string]interface{}{"mimeType": "application/vnd.google-apps.folder"},
		},
		Content: []byte{},
	}

	src.Files["id2"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "id2",
			Name:    "nested.txt",
			Parents: []string{"id_subdir"},
		},
		Content: []byte("nested content"),
	}

	src.Files["id_deep"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "id_deep",
			Name:    "deep",
			Type:    core.FileTypeFolder,
			Parents: []string{"id_subdir"},
			Extra:   map[string]interface{}{"mimeType": "application/vnd.google-apps.folder"},
		},
		Content: []byte{},
	}

	src.Files["id3"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "id3",
			Name:    "file.txt",
			Parents: []string{"id_deep"},
		},
		Content: []byte("deep content"),
	}

	bkMgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil, WithVerbose())
	if _, err := bkMgr.Run(context.Background()); err != nil {
		t.Fatalf("Backup setup failed: %v", err)
	}
	return dest
}

// zipEntries returns a map of filename -> content for all entries in a ZIP archive.
func zipEntries(t *testing.T, buf *bytes.Buffer) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("Failed to open zip: %v", err)
	}
	entries := make(map[string]string)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("Failed to open zip entry %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		entries[f.Name] = string(data)
	}
	return entries
}

func TestRestoreManager_Run(t *testing.T) {
	src := NewMockSource()
	dest := NewMockStore()

	src.AddFile("restore_me.txt", "id1", []byte("restore content"))

	src.Files["id_subdir"] = MockFile{
		Meta: core.FileMeta{
			FileID: "id_subdir",
			Name:   "subdir",
			Type:   core.FileTypeFolder,
			Extra:  map[string]interface{}{"mimeType": "application/vnd.google-apps.folder"},
		},
		Content: []byte{},
	}

	src.Files["id2"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "id2",
			Name:    "nested.txt",
			Parents: []string{"id_subdir"},
		},
		Content: []byte("nested content"),
	}

	bkMgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil, WithVerbose())
	if _, err := bkMgr.Run(context.Background()); err != nil {
		t.Fatalf("Backup setup failed: %v", err)
	}

	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	var buf bytes.Buffer
	result, err := rsMgr.Run(context.Background(), NewZipRestoreWriter(&buf), "")
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if result.FilesWritten < 2 {
		t.Errorf("Expected at least 2 files written, got %d", result.FilesWritten)
	}
	if result.BytesWritten == 0 {
		t.Error("Expected non-zero bytes written")
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("Failed to open zip: %v", err)
	}

	entries := make(map[string]string)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("Failed to open zip entry %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		entries[f.Name] = string(data)
	}

	if content, ok := entries["restore_me.txt"]; !ok {
		t.Error("restore_me.txt not found in zip")
	} else if content != "restore content" {
		t.Errorf("Content mismatch for restore_me.txt: got %q", content)
	}

	if content, ok := entries["subdir/nested.txt"]; !ok {
		t.Error("subdir/nested.txt not found in zip")
	} else if content != "nested content" {
		t.Errorf("Content mismatch for nested.txt: got %q", content)
	}

	if _, ok := entries["subdir/"]; !ok {
		t.Error("subdir/ directory entry not found in zip")
	}
}

func TestRestoreManager_PathFilter_SingleFile(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	var buf bytes.Buffer
	result, err := rsMgr.Run(context.Background(), NewZipRestoreWriter(&buf), "", WithRestorePath("subdir/nested.txt"))
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if result.FilesWritten != 1 {
		t.Errorf("Expected 1 file written, got %d", result.FilesWritten)
	}

	entries := zipEntries(t, &buf)

	if content, ok := entries["subdir/nested.txt"]; !ok {
		t.Error("subdir/nested.txt not found in zip")
	} else if content != "nested content" {
		t.Errorf("Content mismatch: got %q", content)
	}

	// The parent dir "subdir/" should be included as an ancestor.
	if _, ok := entries["subdir/"]; !ok {
		t.Error("ancestor dir subdir/ not found in zip")
	}

	// Other files must not appear.
	if _, ok := entries["restore_me.txt"]; ok {
		t.Error("restore_me.txt should not be in filtered zip")
	}
	if _, ok := entries["subdir/deep/file.txt"]; ok {
		t.Error("subdir/deep/file.txt should not be in filtered zip")
	}
}

func TestRestoreManager_RunToDir(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	outDir := filepath.Join(t.TempDir(), "restored")
	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}
	result, err := rsMgr.Run(context.Background(), writer, "")
	if err != nil {
		t.Fatalf("RunToDir failed: %v", err)
	}
	if result.FilesWritten < 3 {
		t.Fatalf("expected at least 3 files, got %d", result.FilesWritten)
	}

	b, err := os.ReadFile(filepath.Join(outDir, "restore_me.txt"))
	if err != nil {
		t.Fatalf("read restore_me.txt: %v", err)
	}
	if string(b) != "restore content" {
		t.Fatalf("restore_me.txt content=%q", string(b))
	}

	b, err = os.ReadFile(filepath.Join(outDir, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("read subdir/nested.txt: %v", err)
	}
	if string(b) != "nested content" {
		t.Fatalf("nested.txt content=%q", string(b))
	}

	if _, err := os.Stat(filepath.Join(outDir, "subdir", "deep")); err != nil {
		t.Fatalf("expected deep directory: %v", err)
	}
}

func TestRestoreManager_RunToDir_PathFilter(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	outDir := filepath.Join(t.TempDir(), "restored")
	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}
	result, err := rsMgr.Run(context.Background(), writer, "", WithRestorePath("subdir/nested.txt"))
	if err != nil {
		t.Fatalf("RunToDir failed: %v", err)
	}
	if result.FilesWritten != 1 {
		t.Fatalf("expected 1 file, got %d", result.FilesWritten)
	}

	if _, err := os.Stat(filepath.Join(outDir, "subdir", "nested.txt")); err != nil {
		t.Fatalf("expected restored file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "restore_me.txt")); err == nil {
		t.Fatal("restore_me.txt should not be restored with path filter")
	}
}

func TestRestoreManager_RunToDir_SkipsExistingFiles(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	outDir := filepath.Join(t.TempDir(), "restored")
	if err := os.MkdirAll(filepath.Join(outDir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "restore_me.txt"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("write restore_me: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "subdir", "nested.txt"), []byte("existing nested"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}
	result, err := rsMgr.Run(context.Background(), writer, "")
	if err != nil {
		t.Fatalf("RunToDir failed: %v", err)
	}
	if result.Warnings == 0 {
		t.Fatal("expected warnings for skipped existing files")
	}
	if got, err := os.ReadFile(filepath.Join(outDir, "restore_me.txt")); err != nil || string(got) != "existing" {
		t.Fatalf("restore_me.txt = %q, %v", string(got), err)
	}
	if got, err := os.ReadFile(filepath.Join(outDir, "subdir", "nested.txt")); err != nil || string(got) != "existing nested" {
		t.Fatalf("nested.txt = %q, %v", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "subdir", "deep", "file.txt")); err != nil {
		t.Fatalf("expected missing file to be restored: %v", err)
	}
	if result.FilesWritten == 0 {
		t.Fatal("expected at least one file to be restored")
	}
}

func TestFSRestoreWriter_WarnDedupf(t *testing.T) {
	writer, err := NewFSRestoreWriter(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}
	fw := writer.(*fsRestoreWriter)
	var warnings []string
	fw.SetWarningFunc(func(msg string) { warnings = append(warnings, msg) })

	fw.warnDedupf("could not set xattr %q: %v", "com.apple.provenance", os.ErrPermission)
	fw.warnDedupf("could not set xattr %q: %v", "com.apple.provenance", os.ErrPermission)

	if len(warnings) != 1 {
		t.Fatalf("warnings=%d want=1: %#v", len(warnings), warnings)
	}
}

func TestRestoreManager_RunToDir_DryRun_NoWrites(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	outDir := filepath.Join(t.TempDir(), "dry-run-out")
	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}
	result, err := rsMgr.Run(context.Background(), writer, "", WithRestoreDryRun())
	if err != nil {
		t.Fatalf("RunToDir dry-run failed: %v", err)
	}
	if !result.DryRun {
		t.Fatal("expected dry-run result")
	}
	if _, err := os.Stat(outDir); err == nil {
		t.Fatal("dry-run should not create output directory")
	}
}

func TestRestoreManager_Run_NilWriter(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	if _, err := rsMgr.Run(context.Background(), nil, ""); err == nil {
		t.Fatal("expected error for nil writer")
	}
}

func TestFSRestoreWriter_RejectsSymlinkedPathComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	linkPath := filepath.Join(root, "subdir", "link")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	writer, err := NewFSRestoreWriter(root)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter: %v", err)
	}

	err = writer.WriteFile("subdir/link/escaped.txt", core.FileMeta{}, func(w io.Writer) error {
		_, writeErr := w.Write([]byte("data"))
		return writeErr
	})
	if err == nil {
		t.Fatal("expected symlink path to be rejected")
	}

	if _, statErr := os.Stat(filepath.Join(outside, "escaped.txt")); statErr == nil {
		t.Fatal("file unexpectedly written outside restore root via symlink")
	}
}

func TestSecureRestorePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")

	got, err := secureRestorePath(root, "subdir/file.txt")
	if err != nil {
		t.Fatalf("secureRestorePath valid path: %v", err)
	}
	if got != filepath.Join(root, "subdir", "file.txt") {
		t.Fatalf("path=%q", got)
	}

	got, err = secureRestorePath(root, "../../etc/passwd")
	if err != nil {
		t.Fatalf("secureRestorePath traversal normalization: %v", err)
	}
	if got != filepath.Join(root, "etc", "passwd") {
		t.Fatalf("normalized traversal path=%q", got)
	}
}

func TestRestoreManager_PathFilter_Subtree(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	var buf bytes.Buffer
	result, err := rsMgr.Run(context.Background(), NewZipRestoreWriter(&buf), "", WithRestorePath("subdir/"))
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// subdir contains: nested.txt and deep/file.txt -> 2 files.
	if result.FilesWritten != 2 {
		t.Errorf("Expected 2 files written, got %d", result.FilesWritten)
	}

	entries := zipEntries(t, &buf)

	if _, ok := entries["subdir/nested.txt"]; !ok {
		t.Error("subdir/nested.txt not found in zip")
	}
	if _, ok := entries["subdir/deep/file.txt"]; !ok {
		t.Error("subdir/deep/file.txt not found in zip")
	}
	if _, ok := entries["subdir/"]; !ok {
		t.Error("subdir/ dir entry not found in zip")
	}
	if _, ok := entries["subdir/deep/"]; !ok {
		t.Error("subdir/deep/ dir entry not found in zip")
	}

	// Root-level files must not appear.
	if _, ok := entries["restore_me.txt"]; ok {
		t.Error("restore_me.txt should not be in filtered zip")
	}
}

func TestRestoreManager_PathFilter_NoMatch(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	var buf bytes.Buffer
	result, err := rsMgr.Run(context.Background(), NewZipRestoreWriter(&buf), "", WithRestorePath("nonexistent.txt"))
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if result.FilesWritten != 0 {
		t.Errorf("Expected 0 files, got %d", result.FilesWritten)
	}
	if result.DirsWritten != 0 {
		t.Errorf("Expected 0 dirs, got %d", result.DirsWritten)
	}
}

// TestRestoreManager_PathFilter_DeepSingleFile verifies that filtering for a
// deeply nested file (3+ levels) includes ALL ancestor directories, not just
// the immediate parent.
func TestRestoreManager_PathFilter_DeepSingleFile(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	var buf bytes.Buffer
	result, err := rsMgr.Run(context.Background(), NewZipRestoreWriter(&buf), "", WithRestorePath("subdir/deep/file.txt"))
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if result.FilesWritten != 1 {
		t.Errorf("Expected 1 file written, got %d", result.FilesWritten)
	}

	entries := zipEntries(t, &buf)

	if content, ok := entries["subdir/deep/file.txt"]; !ok {
		t.Error("subdir/deep/file.txt not found in zip")
	} else if content != "deep content" {
		t.Errorf("Content mismatch: got %q", content)
	}

	// Both ancestor dirs must be included.
	if _, ok := entries["subdir/"]; !ok {
		t.Error("ancestor dir subdir/ not found in zip")
	}
	if _, ok := entries["subdir/deep/"]; !ok {
		t.Error("ancestor dir subdir/deep/ not found in zip")
	}

	// Siblings must not appear.
	if _, ok := entries["restore_me.txt"]; ok {
		t.Error("restore_me.txt should not be in filtered zip")
	}
	if _, ok := entries["subdir/nested.txt"]; ok {
		t.Error("subdir/nested.txt should not be in filtered zip")
	}
}

// TestRestoreManager_PathFilter_CloudLikeIDs simulates a Google Drive / OneDrive
// backup where FileIDs and Parents are opaque API identifiers (not filesystem
// paths). Verifies that selective restore works correctly with such metadata.
func TestRestoreManager_PathFilter_CloudLikeIDs(t *testing.T) {
	src := NewMockSource()
	dest := NewMockStore()

	// Simulate a cloud drive tree:
	//   My Documents/             (id: FOLDER_AAA)
	//   My Documents/Photos/      (id: FOLDER_BBB)
	//   My Documents/Photos/img.jpg  (id: FILE_CCC)
	//   My Documents/report.pdf   (id: FILE_DDD)
	//   Music/                    (id: FOLDER_EEE)
	//   Music/song.mp3            (id: FILE_FFF)

	src.Files["FOLDER_AAA"] = MockFile{
		Meta: core.FileMeta{
			FileID: "FOLDER_AAA",
			Name:   "My Documents",
			Type:   core.FileTypeFolder,
			Paths:  []string{"My Documents"},
			Extra:  map[string]interface{}{"mimeType": "application/vnd.google-apps.folder"},
		},
	}
	src.Files["FOLDER_BBB"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "FOLDER_BBB",
			Name:    "Photos",
			Type:    core.FileTypeFolder,
			Parents: []string{"FOLDER_AAA"},
			Paths:   []string{"My Documents/Photos"},
			Extra:   map[string]interface{}{"mimeType": "application/vnd.google-apps.folder"},
		},
	}
	src.Files["FILE_CCC"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "FILE_CCC",
			Name:    "img.jpg",
			Parents: []string{"FOLDER_BBB"},
			Paths:   []string{"My Documents/Photos/img.jpg"},
		},
		Content: []byte("jpeg-data"),
	}
	src.Files["FILE_DDD"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "FILE_DDD",
			Name:    "report.pdf",
			Parents: []string{"FOLDER_AAA"},
			Paths:   []string{"My Documents/report.pdf"},
		},
		Content: []byte("pdf-data"),
	}
	src.Files["FOLDER_EEE"] = MockFile{
		Meta: core.FileMeta{
			FileID: "FOLDER_EEE",
			Name:   "Music",
			Type:   core.FileTypeFolder,
			Paths:  []string{"Music"},
			Extra:  map[string]interface{}{"mimeType": "application/vnd.google-apps.folder"},
		},
	}
	src.Files["FILE_FFF"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "FILE_FFF",
			Name:    "song.mp3",
			Parents: []string{"FOLDER_EEE"},
			Paths:   []string{"Music/song.mp3"},
		},
		Content: []byte("mp3-data"),
	}

	bkMgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	if _, err := bkMgr.Run(context.Background()); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	t.Run("subtree filter", func(t *testing.T) {
		rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())
		var buf bytes.Buffer
		result, err := rsMgr.Run(context.Background(), NewZipRestoreWriter(&buf), "", WithRestorePath("My Documents/"))
		if err != nil {
			t.Fatalf("Restore failed: %v", err)
		}

		if result.FilesWritten != 2 {
			t.Errorf("Expected 2 files (img.jpg + report.pdf), got %d", result.FilesWritten)
		}

		entries := zipEntries(t, &buf)
		if _, ok := entries["My Documents/Photos/img.jpg"]; !ok {
			t.Error("My Documents/Photos/img.jpg not found")
		}
		if _, ok := entries["My Documents/report.pdf"]; !ok {
			t.Error("My Documents/report.pdf not found")
		}
		if _, ok := entries["Music/song.mp3"]; ok {
			t.Error("Music/song.mp3 should not be in filtered zip")
		}
	})

	t.Run("single deep file", func(t *testing.T) {
		rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())
		var buf bytes.Buffer
		result, err := rsMgr.Run(context.Background(), NewZipRestoreWriter(&buf), "", WithRestorePath("My Documents/Photos/img.jpg"))
		if err != nil {
			t.Fatalf("Restore failed: %v", err)
		}

		if result.FilesWritten != 1 {
			t.Errorf("Expected 1 file, got %d", result.FilesWritten)
		}

		entries := zipEntries(t, &buf)
		if _, ok := entries["My Documents/Photos/img.jpg"]; !ok {
			t.Error("My Documents/Photos/img.jpg not found")
		}
		// Both ancestor dirs must be included.
		if _, ok := entries["My Documents/"]; !ok {
			t.Error("ancestor My Documents/ not found")
		}
		if _, ok := entries["My Documents/Photos/"]; !ok {
			t.Error("ancestor My Documents/Photos/ not found")
		}
		// Siblings must not appear.
		if _, ok := entries["My Documents/report.pdf"]; ok {
			t.Error("report.pdf should not be in filtered zip")
		}
	})
}

func TestRestoreManager_PathFilter_DryRun(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	var buf bytes.Buffer
	result, err := rsMgr.Run(context.Background(), NewZipRestoreWriter(&buf), "", WithRestorePath("subdir/"), WithRestoreDryRun())
	if err != nil {
		t.Fatalf("Restore dry run failed: %v", err)
	}

	if !result.DryRun {
		t.Error("Expected DryRun to be true")
	}
	if result.FilesWritten != 2 {
		t.Errorf("Expected 2 files in dry run, got %d", result.FilesWritten)
	}

	// Buffer should be empty in dry-run mode.
	if buf.Len() != 0 {
		t.Errorf("Expected empty buffer in dry run, got %d bytes", buf.Len())
	}
}

func TestRestoreManager_PathFilter_MixedLegacyAndNormalizedFileMeta(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()

	rootMeta := core.FileMeta{
		FileID: "dir",
		Name:   "docs",
		Type:   core.FileTypeFolder,
		Paths:  []string{"docs"},
	}
	childMeta := core.FileMeta{
		FileID:  "file",
		Name:    "guide.txt",
		Type:    core.FileTypeFile,
		Parents: []string{"dir"},
		Size:    int64(len("guide")),
	}

	rootRef, rootData, err := rootMeta.Ref()
	if err != nil {
		t.Fatalf("root meta ref: %v", err)
	}
	if err := dest.Put(ctx, rootRef, rootData); err != nil {
		t.Fatalf("put root meta: %v", err)
	}

	content := core.Content{Type: core.ObjectTypeContent, Size: childMeta.Size, DataInlineB64: []byte("guide")}
	contentHash, contentData, err := core.ComputeJSONHash(&content)
	if err != nil {
		t.Fatalf("content ref: %v", err)
	}
	contentRef := "content/" + contentHash
	if err := dest.Put(ctx, contentRef, contentData); err != nil {
		t.Fatalf("put content: %v", err)
	}

	childMeta.ContentHash = core.ComputeHash([]byte("guide"))
	childMeta.ContentRef = contentHash
	childRef, childData, err := childMeta.Ref()
	if err != nil {
		t.Fatalf("child meta ref: %v", err)
	}
	if err := dest.Put(ctx, childRef, childData); err != nil {
		t.Fatalf("put child meta: %v", err)
	}

	tree := hamt.NewTree(dest)
	root, err := tree.Insert("", "", rootMeta.FileID, rootRef)
	if err != nil {
		t.Fatalf("insert root: %v", err)
	}
	root, err = tree.Insert(root, rootMeta.FileID, childMeta.FileID, childRef)
	if err != nil {
		t.Fatalf("insert child: %v", err)
	}

	snap := core.Snapshot{Root: root, Seq: 1, Created: "2026-04-02T00:00:00Z"}
	snapHash, snapData, err := core.ComputeJSONHash(&snap)
	if err != nil {
		t.Fatalf("snapshot ref: %v", err)
	}
	snapRef := "snapshot/" + snapHash
	if err := dest.Put(ctx, snapRef, snapData); err != nil {
		t.Fatalf("put snapshot: %v", err)
	}
	if err := dest.Put(ctx, "index/latest", createIndex(snapRef, 1)); err != nil {
		t.Fatalf("put latest index: %v", err)
	}

	rsMgr := NewRestoreManager(dest, ui.NewNoOpReporter())
	var buf bytes.Buffer
	result, err := rsMgr.Run(ctx, NewZipRestoreWriter(&buf), "latest", WithRestorePath("docs/guide.txt"))
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if result.FilesWritten != 1 {
		t.Fatalf("Expected 1 file, got %d", result.FilesWritten)
	}

	entries := zipEntries(t, &buf)
	if _, ok := entries["docs/guide.txt"]; !ok {
		t.Fatal("docs/guide.txt not restored")
	}
	if _, ok := entries["docs/"]; !ok {
		t.Fatal("docs/ ancestor directory not restored")
	}
}
