package e2e

import (
	"strings"
	"testing"
)

func TestCLI_Feature_IntegrityCheck(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name: "integrity_check",
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("file1.txt", "hello world")
			h.writeFile("secret.txt", "classified data")

			h.initEncrypted()
			h.backup()
			h.writeFile("secret.txt", "updated classified data")
			h.backup()

			out := h.check()
			if !strings.Contains(out, "repository is healthy") {
				t.Errorf("expected healthy check output, got: %s", out)
			}

			out = h.check("--read-data")
			if !strings.Contains(out, "repository is healthy") {
				t.Errorf("expected healthy check --read-data output, got: %s", out)
			}
			if !strings.Contains(out, "Snapshots checked:") {
				t.Errorf("expected check summary in output, got: %s", out)
			}
		},
	})
}
