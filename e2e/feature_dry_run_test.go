package e2e

import "testing"

// TestCLI_Feature_BackupDryRun verifies that `backup -dry-run` reports what
// would be uploaded without actually writing any data to the store.
func TestCLI_Feature_BackupDryRun(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "backup_dry_run",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("file.txt", "content").MustInitEncrypted()

			r.Backup("-dry-run").MustContainAnyFold("dry-run", "dry run")

			// No snapshot should have been written.
			r.List().MustHaveSnapshotCount(0)
		},
	})
}

// TestCLI_Feature_ForgetDryRun verifies that `forget -dry-run` reports snapshots
// it would remove without actually removing them.
// This test complements the policy test in feature_retention_test.go by focusing
// specifically on dry-run output semantics.
func TestCLI_Feature_ForgetDryRun(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "forget_dry_run",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("file.txt", "v1").MustInitEncrypted()
			r.Backup()

			r.WithFile("file.txt", "v2").Backup()

			r.Forget("--keep-last", "1", "--dry-run").MustContain("would remove")

			// Both snapshots should still exist after dry-run.
			r.List().MustHaveSnapshotCount(2)
		},
	})
}
