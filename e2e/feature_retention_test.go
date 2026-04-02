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
			r := h.WithFile("file1.txt", "hello world").MustInitEncrypted()
			r.Backup()

			r.WithFile("file2.txt", "new file").Backup()

			r.Forget("--keep-last", "1", "--prune").
				MustRemove(1).
				MustContain("Objects deleted:").
				MustContain("Space reclaimed:")

			r.List().MustHaveSnapshotCount(1)
		},
	})
}

func TestCLI_Feature_ForgetDryRunPolicy(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "forget_policy_dry_run",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("policy-file.txt", "x").MustInitEncrypted()

			for i := range 3 {
				r.WithFile("policy-file.txt", strings.Repeat("x", i+1)).Backup()
			}

			r.Forget("--keep-last", "1", "--dry-run").
				MustBeDryRun().
				MustWouldRemove(2)

			r.Forget("--keep-last", "1", "--prune").MustRemove(2)
			r.List().MustHaveSnapshotCount(1)
		},
	})
}
