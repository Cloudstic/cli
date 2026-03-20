package e2e

import "testing"

func TestCLI_Feature_IntegrityCheck(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name: "integrity_check",
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.
				WithFile("file1.txt", "hello world").
				WithFile("secret.txt", "classified data").
				MustInitEncrypted()

			r.Backup()
			r.WithFile("secret.txt", "updated classified data").Backup()

			r.Check().MustContain("repository is healthy")

			r.Check("--read-data").
				MustContain("repository is healthy").
				MustContain("Snapshots checked:")
		},
	})
}
