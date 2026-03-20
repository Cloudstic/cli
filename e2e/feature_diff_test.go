package e2e

import "testing"

// TestCLI_Feature_Diff verifies that `cloudstic diff` correctly identifies
// added and modified files between two snapshots.
// Diff is a metadata-only operation; local store is sufficient.
func TestCLI_Feature_Diff(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "diff_snapshots",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.
				WithFile("keep.txt", "unchanged").
				WithFile("modify.txt", "original").
				WithFile("remove.txt", "will be deleted").
				MustInitEncrypted()
			r.Backup()

			// Capture snapshot 1 full hash from list output.
			snap1 := r.List().FirstSnapshotID()

			// Mutate the source: modify one file, add one, remove one.
			r.WithFile("modify.txt", "changed content").
				WithFile("add.txt", "new file").
				RemoveFile("remove.txt")
			r.Backup()

			// Diff between first and second (latest) snapshot.
			r.Diff(snap1, "latest").
				MustHaveChange("added", "add.txt").
				MustHaveChange("modified", "modify.txt").
				MustHaveChange("removed", "remove.txt")
		},
	})
}

// TestCLI_Feature_DiffSameSnapshot verifies that diffing a snapshot against
// itself produces no output (no changes).
func TestCLI_Feature_DiffSameSnapshot(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "diff_same_snapshot",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("file.txt", "content").MustInitEncrypted()
			r.Backup()

			snap := r.List().FirstSnapshotID()
			r.Diff(snap, snap).MustHaveNoChanges()
		},
	})
}
