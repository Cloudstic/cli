package e2e

import (
	"strings"
	"testing"
)

func TestCLI_Feature_RetentionAndPrune(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "retention_prune",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("file1.txt", "hello world")
			h.initEncrypted()
			h.backup()

			h.writeFile("file2.txt", "new file")
			h.backup()

			out := h.forget("--keep-last", "1", "--prune")
			if !strings.Contains(out, "Objects deleted:") {
				t.Errorf("expected prune to delete objects, got: %s", out)
			}
			if !strings.Contains(out, "Space reclaimed:") {
				t.Errorf("expected prune to reclaim space, got: %s", out)
			}

			out = h.list()
			if !strings.Contains(out, "1 snapshot") {
				t.Errorf("expected 1 snapshot after prune, got: %s", out)
			}
		},
	})
}

func TestCLI_Feature_ForgetDryRunPolicy(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "forget_policy_dry_run",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("policy-file.txt", "x")
			h.initEncrypted()

			for i := range 3 {
				h.writeFile("policy-file.txt", strings.Repeat("x", i+1))
				h.backup()
			}

			out := h.forget("--keep-last", "1", "--dry-run")
			if !strings.Contains(out, "would remove") {
				t.Errorf("expected dry-run output, got: %s", out)
			}

			h.forget("--keep-last", "1", "--prune")
			out = h.list()
			if !strings.Contains(out, "1 snapshot") {
				t.Errorf("expected 1 snapshot after policy, got: %s", out)
			}
		},
	})
}
