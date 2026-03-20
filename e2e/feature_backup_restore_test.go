package e2e

import (
	"path/filepath"
	"testing"
)

func TestCLI_Feature_BackupRestoreLatest(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name: "backup_restore_latest",
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.
				WithFile("file1.txt", "hello world").
				WithFile("secret.txt", "classified data").
				WithFile("subdir/nested.txt", "nested content").
				MustInitEncrypted()
			r.Backup()

			r.WithFile("file2.txt", "new file").
				WithFile("secret.txt", "updated classified data")
			xattrName, hasXattrValidation := maybeSetTestXattr(t, h.source, "secret.txt", "classified-xattr")
			r.Backup("-tag", "daily", "-tag", "important")

			zipOut := r.RestoreZip("restore.zip")
			for _, tc := range []struct {
				path    string
				content string
			}{
				{path: "file1.txt", content: "hello world"},
				{path: "file2.txt", content: "new file"},
				{path: "secret.txt", content: "updated classified data"},
				{path: "subdir/nested.txt", content: "nested content"},
			} {
				zipOut.MustHaveFileContent(tc.path, tc.content)
			}

			dirOut := r.RestoreDir("restore-dir")
			for _, tc := range []struct {
				path    string
				content string
			}{
				{path: "file1.txt", content: "hello world"},
				{path: "file2.txt", content: "new file"},
				{path: "secret.txt", content: "updated classified data"},
				{path: "subdir/nested.txt", content: "nested content"},
			} {
				dirOut.MustHaveFileContent(tc.path, tc.content)
			}
			if hasXattrValidation {
				assertXattrValue(t, filepath.Join(dirOut.Path(), "secret.txt"), xattrName, "classified-xattr")
			}
		},
	})
}
