package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLI_Feature_BackupRestoreLatest(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name: "backup_restore_latest",
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("file1.txt", "hello world")
			h.writeFile("secret.txt", "classified data")
			h.writeFile("subdir/nested.txt", "nested content")

			h.initEncrypted()
			h.backup()

			h.writeFile("file2.txt", "new file")
			h.writeFile("secret.txt", "updated classified data")
			xattrName, hasXattrValidation := maybeSetTestXattr(t, h.source, "secret.txt", "classified-xattr")
			h.backup("-tag", "daily", "-tag", "important")

			zipPath := h.restoreZip("restore.zip")
			for _, tc := range []struct {
				path    string
				content string
			}{
				{path: "file1.txt", content: "hello world"},
				{path: "file2.txt", content: "new file"},
				{path: "secret.txt", content: "updated classified data"},
				{path: "subdir/nested.txt", content: "nested content"},
			} {
				if got := readZipFile(t, zipPath, tc.path); got != tc.content {
					t.Errorf("restore content mismatch for %s: got %q, want %q", tc.path, got, tc.content)
				}
			}

			dirOut := h.restoreDir("restore-dir")
			for _, tc := range []struct {
				path    string
				content string
			}{
				{path: "file1.txt", content: "hello world"},
				{path: "file2.txt", content: "new file"},
				{path: "secret.txt", content: "updated classified data"},
				{path: "subdir/nested.txt", content: "nested content"},
			} {
				contentPath := filepath.Join(dirOut, filepath.FromSlash(tc.path))
				b, err := os.ReadFile(contentPath)
				if err != nil {
					t.Errorf("direct restore missing %s: %v", tc.path, err)
					continue
				}
				if got := string(b); got != tc.content {
					t.Errorf("direct restore content mismatch for %s: got %q, want %q", tc.path, got, tc.content)
				}
			}
			if hasXattrValidation {
				assertXattrValue(t, filepath.Join(dirOut, "secret.txt"), xattrName, "classified-xattr")
			}
		},
	})
}
