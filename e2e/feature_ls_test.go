package e2e

import "testing"

// TestCLI_Feature_Ls verifies that `cloudstic ls` lists all files in the
// latest snapshot with the expected paths. Ls is metadata-only; local store is sufficient.
func TestCLI_Feature_Ls(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "ls_latest",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.
				WithFile("docs/readme.md", "# Readme").
				WithFile("docs/guide.md", "# Guide").
				WithFile("src/main.go", "package main").
				MustInitEncrypted()
			r.Backup()
			r.Ls().
				MustContainEntry("readme.md").
				MustContainEntry("guide.md").
				MustContainEntry("main.go")
		},
	})
}

// TestCLI_Feature_LsBySnapshotID verifies that `cloudstic ls <id>` resolves
// a specific snapshot by its prefix.
func TestCLI_Feature_LsBySnapshotID(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "ls_by_snapshot_id",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("snap1-only.txt", "only in snap1").MustInitEncrypted()
			r.Backup()

			snap1 := r.List().FirstSnapshotID()

			// Add another file and take a second snapshot.
			r.WithFile("snap2-only.txt", "only in snap2").Backup()

			// ls on the first snapshot should NOT have snap2-only.txt.
			r.Ls(snap1).
				MustNotContainEntry("snap2-only.txt").
				MustContainEntry("snap1-only.txt")
		},
	})
}
