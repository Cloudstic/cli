package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

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

	bkMgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), WithVerbose())
	if _, err := bkMgr.Run(context.Background()); err != nil {
		t.Fatalf("Backup setup failed: %v", err)
	}

	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())

	var buf bytes.Buffer
	result, err := rsMgr.Run(context.Background(), &buf, "")
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
