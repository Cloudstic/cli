package e2e

import "testing"

func TestCLI_Feature_PartialRestore(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name: "partial_restore",
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.WithFile("file1.txt", "hello world").
				WithFile("file2.txt", "new file").
				WithFile("subdir/nested.txt", "nested content")

			r := h.MustInitEncrypted()
			r.Backup()

			r.RestoreZip("partial_file.zip", "-path", "file1.txt").
				MustHaveFileContent("file1.txt", "hello world").
				MustNotContainFile("file2.txt").
				MustNotContainFile("subdir/nested.txt")

			r.RestoreZip("partial_subtree.zip", "-path", "subdir/").
				MustHaveFileContent("subdir/nested.txt", "nested content").
				MustNotContainFile("file1.txt").
				MustNotContainFile("file2.txt")

			r.RestoreDir("partial-dir", "-path", "file1.txt").
				MustHaveFileContent("file1.txt", "hello world").
				MustNotContainFile("file2.txt")
		},
	})
}
