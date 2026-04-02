package e2e

import "testing"

// TestCLI_Feature_BreakLock verifies that `break-lock` succeeds on a repository
// that has no active lock. After a clean backup completes, there should be no stale
// lock file, so break-lock should report that no lock was found (or exit cleanly).
func TestCLI_Feature_BreakLock(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "break_lock_no_stale_lock",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("file.txt", "content").MustInitEncrypted()
			r.Backup()

			// After a clean backup the lock is released. break-lock should
			// exit successfully and indicate no lock was present.
			r.BreakLock().
				MustNotContainFold("error").
				MustContainAnyFold("no lock", "not locked")
		},
	})
}
