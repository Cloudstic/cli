package e2e

import (
	"strings"
	"testing"
)

func TestCLI_CompatibilitySmokeMatrix(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name: "compatibility_smoke",
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("file1.txt", "hello world")
			h.writeFile("subdir/nested.txt", "nested content")

			h.initEncrypted()
			h.backup()

			out := h.list()
			if !strings.Contains(out, "1 snapshot") {
				t.Fatalf("expected 1 snapshot, got: %s", out)
			}

			zipPath := h.restoreZip("restore.zip")
			if got := readZipFile(t, zipPath, "file1.txt"); got != "hello world" {
				t.Fatalf("restore mismatch for file1.txt: got %q, want %q", got, "hello world")
			}
			if got := readZipFile(t, zipPath, "subdir/nested.txt"); got != "nested content" {
				t.Fatalf("restore mismatch for subdir/nested.txt: got %q, want %q", got, "nested content")
			}
		},
	})
}
