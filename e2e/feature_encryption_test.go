package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCLI_Feature_EncryptedRepoErrors(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "encrypted_repo_errors",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("file1.txt", "hello world")
			h.initEncrypted()
			h.backup()

			out := runExpectFail(t, h.bin, append([]string{"list", "--password", "wrong-password"}, h.storeArgs...)...)
			if !strings.Contains(out, "no provided credential matches") {
				t.Errorf("expected credential mismatch error, got: %s", out)
			}

			out = runExpectFail(t, h.bin, append([]string{"list"}, h.storeArgs...)...)
			if !strings.Contains(out, "repository is encrypted") {
				t.Errorf("expected encrypted-repo error, got: %s", out)
			}
		},
	})
}

func TestCLI_Feature_InitRequiresEncryption(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)
	dummyStoreDir := t.TempDir()
	dummyStoreArgs := []string{"--store", "local:" + dummyStoreDir}
	out := runExpectFail(t, bin, append([]string{"init"}, dummyStoreArgs...)...)
	if !strings.Contains(out, "encryption is required") {
		t.Errorf("expected encryption-required error, got: %s", out)
	}
}

func TestCLI_Feature_RecoveryKeyRoundTrip(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "recovery_key_round_trip",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("file1.txt", "hello world")
			out := h.initEncrypted("--adopt-slots", "--add-recovery-key")
			if !strings.Contains(out, "RECOVERY KEY") {
				t.Fatalf("expected recovery key output on init, got: %s", out)
			}

			mnemonic := extractMnemonic(t, out)
			if mnemonic == "" {
				t.Fatal("could not extract mnemonic from recovery key output")
			}

			h.backup()

			zipPath := filepath.Join(h.restoreRoot, "recovery_restore.zip")
			args := append([]string{"restore", "--output", zipPath, "--recovery-key", mnemonic}, h.storeArgs...)
			run(t, h.bin, args...)

			if got := readZipFile(t, zipPath, "file1.txt"); got != "hello world" {
				t.Errorf("recovery restore content mismatch for file1.txt: got %q, want %q", got, "hello world")
			}
		},
	})
}

func TestCLI_Feature_UnencryptedLifecycle(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "unencrypted_lifecycle",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			h.writeFile("file1.txt", "hello world")
			h.writeFile("secret.txt", "updated classified data")
			h.writeFile("subdir/nested.txt", "nested content")

			out := h.initUnencrypted()
			if !strings.Contains(out, "encrypted: false") {
				t.Errorf("expected 'encrypted: false' in init output, got: %s", out)
			}

			h.backupUnencrypted()
			out = h.listUnencrypted()
			if !strings.Contains(out, "1 snapshot") {
				t.Fatalf("unencrypted: expected 1 snapshot, got: %s", out)
			}

			h.writeFile("unenc-file.txt", "plaintext content")
			h.backupUnencrypted()
			out = h.listUnencrypted()
			if !strings.Contains(out, "2 snapshots") {
				t.Fatalf("unencrypted: expected 2 snapshots, got: %s", out)
			}

			zipPath := h.restoreZipUnencrypted("unenc_restore.zip")
			for _, tc := range []struct {
				path    string
				content string
			}{
				{path: "file1.txt", content: "hello world"},
				{path: "secret.txt", content: "updated classified data"},
				{path: "subdir/nested.txt", content: "nested content"},
				{path: "unenc-file.txt", content: "plaintext content"},
			} {
				if got := readZipFile(t, zipPath, tc.path); got != tc.content {
					t.Errorf("unencrypted restore mismatch for %s: got %q, want %q", tc.path, got, tc.content)
				}
			}

			out = h.checkUnencrypted("--read-data")
			if !strings.Contains(out, "repository is healthy") {
				t.Errorf("unencrypted: expected healthy check output, got: %s", out)
			}

			out = h.forgetUnencrypted("--keep-last", "1", "--prune")
			if !strings.Contains(out, "Objects deleted:") {
				t.Errorf("unencrypted: expected prune to delete objects, got: %s", out)
			}
			if !strings.Contains(out, "Space reclaimed:") {
				t.Errorf("unencrypted: expected prune to reclaim space, got: %s", out)
			}

			out = h.listUnencrypted()
			if !strings.Contains(out, "1 snapshot") {
				t.Errorf("unencrypted: expected 1 snapshot after prune, got: %s", out)
			}
		},
	})
}
