package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCLI_Feature_EncryptedRepoErrors verifies that accessing an encrypted
// repository with no credentials or the wrong password produces clear errors.
func TestCLI_Feature_EncryptedRepoErrors(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "encrypted_repo_errors",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("file1.txt", "hello world").MustInitEncrypted()
			r.Backup()

			// Wrong password must produce a credential-mismatch error.
			h.RunExpectFail(append([]string{"list", "--password", "wrong-password"}, h.storeArgs...)...).
				MustContain("no provided credential matches")

			// No credentials at all must produce an encrypted-repo error.
			h.RunExpectFail(append([]string{"list"}, h.storeArgs...)...).
				MustContain("repository is encrypted")
		},
	})
}

// TestCLI_Feature_InitRequiresEncryption verifies that `init` without any
// encryption option fails with an actionable error message.
func TestCLI_Feature_InitRequiresEncryption(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "init_requires_encryption",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.RunExpectFail(append([]string{"init"}, h.storeArgs...)...).
				MustContain("encryption is required")
		},
	})
}

// TestCLI_Feature_RecoveryKeyRoundTrip verifies the full recovery-key flow:
// init with a BIP39 recovery key → backup → restore using only the mnemonic.
func TestCLI_Feature_RecoveryKeyRoundTrip(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "recovery_key_round_trip",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.WithFile("file1.txt", "hello world")

			r, out := h.InitEncrypted("--adopt-slots", "--add-recovery-key")
			if !strings.Contains(out, "RECOVERY KEY") {
				t.Fatalf("expected recovery key output on init, got: %s", out)
			}

			mnemonic := extractMnemonic(t, out)
			if mnemonic == "" {
				t.Fatal("could not extract mnemonic from recovery key output")
			}

			r.Backup()

			// Restore using only the recovery key — no password required.
			zipPath := filepath.Join(r.h.restoreRoot, "recovery_restore.zip")
			h.Run(
				append([]string{"restore", "--output", zipPath, "--recovery-key", mnemonic},
					r.h.storeArgs...)...)
			(&restoreZipResult{t: t, zipPath: zipPath}).MustHaveFileContent("file1.txt", "hello world")
		},
	})
}

// TestCLI_Feature_UnencryptedLifecycle exercises the full backup/restore/check/prune
// lifecycle on a repository that was intentionally initialised without encryption.
// Using initUnencrypted() switches the harness authArgs to bare store args,
// so all subsequent harness calls (backup, list, check, forget) work without
// a password — no separate *Unencrypted methods needed.
func TestCLI_Feature_UnencryptedLifecycle(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "unencrypted_lifecycle",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.WithFile("file1.txt", "hello world").
				WithFile("secret.txt", "updated classified data").
				WithFile("subdir/nested.txt", "nested content")

			r, out := h.InitUnencrypted()
			if !strings.Contains(out, "encrypted: false") {
				t.Fatalf("expected 'encrypted: false' in init output, got: %s", out)
			}

			r.Backup()
			r.List().MustHaveSnapshotCount(1)

			r.WithFile("unenc-file.txt", "plaintext content").Backup()
			r.List().MustHaveSnapshotCount(2)

			zipOut := r.RestoreZip("unenc_restore.zip")
			for _, tc := range []struct {
				path    string
				content string
			}{
				{"file1.txt", "hello world"},
				{"secret.txt", "updated classified data"},
				{"subdir/nested.txt", "nested content"},
				{"unenc-file.txt", "plaintext content"},
			} {
				zipOut.MustHaveFileContent(tc.path, tc.content)
			}

			r.Check("--read-data").MustContain("repository is healthy")

			r.Forget("--keep-last", "1", "--prune").
				MustRemove(1).
				MustContain("Objects deleted:").
				MustContain("Space reclaimed:")

			r.List().MustHaveSnapshotCount(1)
		},
	})
}
