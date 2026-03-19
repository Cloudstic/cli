package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLI_Feature_PartialRestore(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name: "partial_restore",
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("file1.txt", "hello world")
			h.writeFile("file2.txt", "new file")
			h.writeFile("subdir/nested.txt", "nested content")

			h.initEncrypted()
			h.backup()

			partialFilePath := h.restoreZip("partial_file.zip", "-path", "file1.txt")
			if got := readZipFile(t, partialFilePath, "file1.txt"); got != "hello world" {
				t.Errorf("partial restore single file mismatch: got %q, want %q", got, "hello world")
			}
			assertZipMissing(t, partialFilePath, "file2.txt")
			assertZipMissing(t, partialFilePath, "subdir/nested.txt")

			partialSubtreePath := h.restoreZip("partial_subtree.zip", "-path", "subdir/")
			if got := readZipFile(t, partialSubtreePath, "subdir/nested.txt"); got != "nested content" {
				t.Errorf("partial restore subtree mismatch: got %q, want %q", got, "nested content")
			}
			assertZipMissing(t, partialSubtreePath, "file1.txt")
			assertZipMissing(t, partialSubtreePath, "file2.txt")

			partialDirOut := h.restoreDir("partial-dir", "-path", "file1.txt")
			b, err := os.ReadFile(filepath.Join(partialDirOut, "file1.txt"))
			if err != nil {
				t.Fatalf("partial dir restore missing file1.txt: %v", err)
			}
			if got := string(b); got != "hello world" {
				t.Errorf("partial dir restore mismatch: got %q, want %q", got, "hello world")
			}
			if _, err := os.Stat(filepath.Join(partialDirOut, "file2.txt")); err == nil {
				t.Errorf("file2.txt should not be present in partial dir restore")
			}
		},
	})
}
