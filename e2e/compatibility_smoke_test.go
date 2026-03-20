package e2e

import "testing"

func TestCLI_CompatibilitySmokeMatrix(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name: "compatibility_smoke",
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.
				WithFile("file1.txt", "hello world").
				WithFile("subdir/nested.txt", "nested content").
				MustInitEncrypted()

			r.Backup()

			r.List().MustHaveSnapshotCount(1)

			r.RestoreZip("restore.zip").
				MustHaveFileContent("file1.txt", "hello world").
				MustHaveFileContent("subdir/nested.txt", "nested content")
		},
	})
}
