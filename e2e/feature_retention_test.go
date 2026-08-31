package e2e

import (
	"strings"
	"testing"
)

func TestCLI_Feature_RetentionAndPrune(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// TestCLI_Feature_ForgetNamedSnapshotOnPackedRepo covers forgetting one named
// snapshot from a packfile repository, which is a different path from the two
// tests above in both halves: it names its target rather than deriving a batch
// from a retention policy, and it reports a failure rather than swallowing it.
//
// The distinction is what let this break unnoticed. A snapshot object is small,
// so the default format bundles it into a packfile and it has no standalone
// object in the store; removing it means rewriting the pack catalog. Sizing the
// object first — which the metering layer does on every delete, to credit the
// bytes back — used to bypass the catalog and ask the backend, which correctly
// reported no such object, and the delete failed before the pack layer saw it.
// A following prune then had nothing unlinked to collect.
func TestCLI_Feature_ForgetNamedSnapshotOnPackedRepo(t *testing.T) {
	t.Parallel()
	runFeatureMatrix(t, featureSpec{
		name:         "forget_named_snapshot_packed",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, _ matrixEntry) {
			r := h.WithFile("keep.txt", "kept across both snapshots").MustInitEncrypted()
			r.Backup()

			// Captured while it is the only snapshot. Both backups land in the
			// same second, so picking the older of the two out of a rendered
			// list afterwards is not reliably a choice at all.
			first := r.List().FirstSnapshotID()

			r.WithFile("later.txt", "only in the second snapshot").Backup()

			// The snapshots really are packed, or the delete below would find a
			// standalone object and pass without the catalog being consulted.
			var packed bool
			for _, key := range storeObjectKeys(t, localStoreDir(t, h)) {
				if strings.HasPrefix(key, "packs/") {
					packed = true
					break
				}
			}
			if !packed {
				t.Fatal("precondition failed: the repository wrote no packfiles")
			}

			r.List().MustHaveSnapshotCount(2)
			r.ForgetSnapshot(first).MustContain("Snapshot removed")

			r.List().MustHaveSnapshotCount(1).MustNotContain(first)
			r.Prune().MustDeleteObjects()

			// What survived is intact and still restorable.
			r.RestoreZip("forget-named.zip").MustHaveFileContent("later.txt", "only in the second snapshot")
			r.Check().MustContain("No errors found")
		},
	})
}
