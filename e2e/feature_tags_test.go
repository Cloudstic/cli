package e2e

import "testing"

// TestCLI_Feature_BackupTags verifies that:
//  1. Tags provided via -tag are visible in `list` output.
//  2. `forget --tag` selectively removes only snapshots with that tag.
//
// Note: `list` does not support filtering by tag — tags are shown inline in
// the snapshot summary. `forget` is the command that supports --tag filtering.
func TestCLI_Feature_BackupTags(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "backup_tags",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("file1.txt", "v1").MustInitEncrypted()

			// Snapshot 1: tagged "daily".
			r.Backup("-tag", "daily")

			r.WithFile("file2.txt", "v2")
			// Snapshot 2: tagged "weekly" and "important".
			r.Backup("-tag", "weekly", "-tag", "important")

			// Both snapshots should exist.
			r.List().
				MustHaveSnapshotCount(2).
				MustHaveTag("daily").
				MustHaveTag("weekly")

			// forget --tag daily removes all snapshots with that tag.
			r.Forget("--tag", "daily", "--prune").MustRemove(1)

			rows := r.List().MustHaveSnapshotCount(1).SnapshotRows()
			// The remaining snapshot should carry the "weekly" tag.
			if len(rows) != 1 {
				t.Fatalf("expected exactly one snapshot row, got %d", len(rows))
			}
			if len(rows[0].Tags) == 0 || rows[0].Tags[0] != "weekly" && rows[0].Tags[0] != "important" {
				t.Fatalf("expected remaining snapshot to keep weekly/important tags, got %+v", rows[0].Tags)
			}
			hasWeekly := false
			for _, tag := range rows[0].Tags {
				if tag == "weekly" {
					hasWeekly = true
					break
				}
			}
			if !hasWeekly {
				t.Fatalf("expected remaining snapshot to carry weekly tag, got %+v", rows[0].Tags)
			}
		},
	})
}
