package main

import (
	"archive/zip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEnv classifies a test integration environment.
type TestEnv string

const (
	// Hermetic runs entirely local (e.g., TempDir, Testcontainers). Safe for all machines.
	Hermetic TestEnv = "hermetic"
	// Live runs against real cloud vendor APIs (e.g., real AWS S3, real Google Drive). Requires secrets.
	Live TestEnv = "live"
)

// currentE2EMode returns the current test mode ("hermetic", "live", "all").
// Defaults to hermetic if unset.
func currentE2EMode() string {
	if mode := os.Getenv("CLOUDSTIC_E2E_MODE"); mode != "" {
		return strings.ToLower(mode)
	}
	return "hermetic"
}

// shouldRun returns true if the given environment should run under the current E2E mode.
func shouldRun(e TestEnv) bool {
	mode := currentE2EMode()
	if mode == "all" {
		return true
	}
	return mode == string(e)
}

func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "cloudstic")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Build failed: %v\n%s", err, out)
	}
	return bin
}

// cleanEnv returns the current environment with all CLOUDSTIC_ variables removed
// so that tests don't inherit credentials from the host.
func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLOUDSTIC_") {
			env = append(env, e)
		}
	}
	return env
}

func run(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Command %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func runExpectFail(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("Expected command %v to fail, but it succeeded:\n%s", args, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// ----------------------------------------------------------------------------
// E2E Testing Matrix Interfaces
// ----------------------------------------------------------------------------

// TestSource encapsulates the origin of data to be backed up.
type TestSource interface {
	Name() string
	Env() TestEnv
	Setup(t *testing.T) (sourceArgs []string)
	WriteFile(t *testing.T, relPath, content string)
}

// TestStore encapsulates the content-addressable storage backend.
type TestStore interface {
	Name() string
	Env() TestEnv
	Setup(t *testing.T) (storeArgs []string)
}

// ----------------------------------------------------------------------------
// Local Filesystem Implementations
// ----------------------------------------------------------------------------

type localSource struct {
	dir string
}

func newLocalSource(t *testing.T) *localSource {
	return &localSource{dir: t.TempDir()}
}

func (s *localSource) Name() string { return "local" }
func (s *localSource) Env() TestEnv { return Hermetic }
func (s *localSource) Setup(t *testing.T) []string {
	return []string{"-source", "local", "-source-path", s.dir}
}
func (s *localSource) WriteFile(t *testing.T, relPath, content string) {
	writeFile(t, s.dir, relPath, content)
}

type localStore struct {
	dir string
}

func newLocalStore(t *testing.T) *localStore {
	return &localStore{dir: t.TempDir()}
}

func (s *localStore) Name() string { return "local" }
func (s *localStore) Env() TestEnv { return Hermetic }
func (s *localStore) Setup(t *testing.T) []string {
	return []string{"-store", "local", "-store-path", s.dir}
}

// ----------------------------------------------------------------------------
// Matrix E2E Test Suite
// ----------------------------------------------------------------------------

func TestCLI_EndToEnd_Matrix(t *testing.T) {
	bin := buildBinary(t)

	// In the future, we will selectively append Google Drive, OneDrive, S3, B2, etc
	// here depending on the environment and available secrets.
	sources := []func(t *testing.T) TestSource{
		func(t *testing.T) TestSource { return newLocalSource(t) },
	}
	stores := []func(t *testing.T) TestStore{
		func(t *testing.T) TestStore { return newLocalStore(t) },
	}

	// Only add Docker-based hermetic tests if Docker is actually available
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		sources = append(sources, func(t *testing.T) TestSource { return newSFTPTestSource(t) })
		stores = append(stores, func(t *testing.T) TestStore { return newMinIOTestStore(t) })
		stores = append(stores, func(t *testing.T) TestStore { return newSFTPTestStore(t) })
	}

	for _, srcFn := range sources {
		for _, storeFn := range stores {
			// We build instances just to read their Name/Env for the test name
			probeSrc := srcFn(t)
			probeStore := storeFn(t)

			if !shouldRun(probeSrc.Env()) || !shouldRun(probeStore.Env()) {
				continue
			}

			t.Run(probeSrc.Name()+"_to_"+probeStore.Name(), func(t *testing.T) {
				t.Parallel()

				// Instantiate fresh environments for this particular isolated test
				src := srcFn(t)
				store := storeFn(t)
				restoreDir := t.TempDir()

				srcArgs := src.Setup(t)
				storeArgs := store.Setup(t)

				password := "test-matrix-passphrase"
				baseEncArgs := append(storeArgs, "-encryption-password", password)

				// 1. Initial State
				src.WriteFile(t, "file1.txt", "hello world")
				src.WriteFile(t, "secret.txt", "classified data")
				src.WriteFile(t, "subdir/nested.txt", "nested content")

				// 2. Init
				run(t, bin, append([]string{"init"}, baseEncArgs...)...)

				// 3. Backup 1
				run(t, bin, append([]string{"backup"}, append(srcArgs, baseEncArgs...)...)...)

				// 4. Verify Backup 1
				out := run(t, bin, append([]string{"list"}, baseEncArgs...)...)
				if !strings.Contains(out, "1 snapshot") {
					t.Fatalf("Expected 1 snapshot, got: %s", out)
				}

				// 5. Incremental State
				src.WriteFile(t, "file2.txt", "new file")
				src.WriteFile(t, "secret.txt", "updated classified data")

				// 6. Backup 2 (with tags)
				backup2Args := append([]string{"backup"}, append(srcArgs, baseEncArgs...)...)
				backup2Args = append(backup2Args, "-tag", "daily", "-tag", "important")
				run(t, bin, backup2Args...)

				// 7. Verify Backup 2
				out = run(t, bin, append([]string{"list"}, baseEncArgs...)...)
				if !strings.Contains(out, "2 snapshots") {
					t.Fatalf("Expected 2 snapshots, got: %s", out)
				}
				if !strings.Contains(out, "daily, important") {
					t.Fatalf("Expected tags 'daily, important' in output: %s", out)
				}

				// 7a. Check repository integrity
				out = run(t, bin, append([]string{"check"}, baseEncArgs...)...)
				if !strings.Contains(out, "repository is healthy") {
					t.Errorf("Expected healthy check output, got: %s", out)
				}

				// 7b. Check with --read-data for full byte-level verification
				out = run(t, bin, append([]string{"check", "--read-data"}, baseEncArgs...)...)
				if !strings.Contains(out, "repository is healthy") {
					t.Errorf("Expected healthy check --read-data output, got: %s", out)
				}
				if !strings.Contains(out, "Snapshots checked:") {
					t.Errorf("Expected check summary in output, got: %s", out)
				}

				// 8. Restore Latest -> Validates bits
				zipPath := filepath.Join(restoreDir, "restore.zip")
				restoreArgs := append([]string{"restore"}, baseEncArgs...)
				restoreArgs = append(restoreArgs, "-output", zipPath)
				run(t, bin, restoreArgs...)

				for _, tc := range []struct {
					path    string
					content string
				}{
					{"file1.txt", "hello world"},
					{"file2.txt", "new file"},
					{"secret.txt", "updated classified data"},
					{"subdir/nested.txt", "nested content"},
				} {
					if got := readZipFile(t, zipPath, tc.path); got != tc.content {
						t.Errorf("Restore content mismatch for %s: got %q, want %q", tc.path, got, tc.content)
					}
				}

				// 8a. Partial Restore — single file
				partialFilePath := filepath.Join(restoreDir, "partial_file.zip")
				partialFileArgs := append([]string{"restore"}, baseEncArgs...)
				partialFileArgs = append(partialFileArgs, "-output", partialFilePath, "-path", "file1.txt")
				run(t, bin, partialFileArgs...)

				if got := readZipFile(t, partialFilePath, "file1.txt"); got != "hello world" {
					t.Errorf("Partial restore single file mismatch: got %q, want %q", got, "hello world")
				}
				// Verify other files are NOT in the zip.
				assertZipMissing(t, partialFilePath, "file2.txt")
				assertZipMissing(t, partialFilePath, "subdir/nested.txt")

				// 8b. Partial Restore — subtree
				partialSubtreePath := filepath.Join(restoreDir, "partial_subtree.zip")
				partialSubtreeArgs := append([]string{"restore"}, baseEncArgs...)
				partialSubtreeArgs = append(partialSubtreeArgs, "-output", partialSubtreePath, "-path", "subdir/")
				run(t, bin, partialSubtreeArgs...)

				if got := readZipFile(t, partialSubtreePath, "subdir/nested.txt"); got != "nested content" {
					t.Errorf("Partial restore subtree mismatch for subdir/nested.txt: got %q, want %q", got, "nested content")
				}
				// Verify root-level files are NOT in the zip.
				assertZipMissing(t, partialSubtreePath, "file1.txt")
				assertZipMissing(t, partialSubtreePath, "file2.txt")

				// 9. Forget & Prune
				forgetArgs := append([]string{"forget", "--keep-last", "1", "--prune"}, baseEncArgs...)
				out = run(t, bin, forgetArgs...)

				// Assert that prune output is visible and space was reclaimed
				if !strings.Contains(out, "Objects deleted:") {
					t.Errorf("Expected prune to delete objects, got: %s", out)
				}
				if !strings.Contains(out, "Space reclaimed:") {
					t.Errorf("Expected prune to reclaim space, got: %s", out)
				}

				run(t, bin, append([]string{"list"}, baseEncArgs...)...)
				// 10. Test Key Validation (Wrong Password)
				out = runExpectFail(t, bin, append([]string{"list", "--encryption-password", "wrong-password"}, storeArgs...)...)
				if !strings.Contains(out, "no provided credential matches") {
					t.Errorf("Expected credential mismatch error, got: %s", out)
				}

				// 11. Test Init Requires Encryption
				// (We use a fresh initialized dummy temp dir for this test to not ruin the matrix store)
				dummyStoreDir := t.TempDir()
				dummyStoreArgs := []string{"--store", "local", "--store-path", dummyStoreDir}
				out = runExpectFail(t, bin, append([]string{"init"}, dummyStoreArgs...)...)
				if !strings.Contains(out, "encryption is required") {
					t.Errorf("Expected encryption-required error, got: %s", out)
				}

				// 12. Test Missing Password on Encrypted Store
				out = runExpectFail(t, bin, append([]string{"list"}, storeArgs...)...)
				if !strings.Contains(out, "repository is encrypted") {
					t.Errorf("Expected encrypted-repo error, got: %s", out)
				}

				// 13. Test Backup Storage Backend With Recovery Key Generation & Restore
				// Re-init the isolated dummy store with recovery key enabled
				out = run(t, bin, append([]string{"init", "--encryption-password", password, "--recovery"}, dummyStoreArgs...)...)
				if !strings.Contains(out, "RECOVERY KEY") {
					t.Fatalf("Expected recovery key output on init, got: %s", out)
				}
				mnemonic := extractMnemonic(t, out)
				if mnemonic == "" {
					t.Fatal("Could not extract mnemonic from recovery key output")
				}

				run(t, bin, append([]string{"backup", "--encryption-password", password}, append(srcArgs, dummyStoreArgs...)...)...)

				zipRecoveryPath := filepath.Join(restoreDir, "recovery_restore.zip")
				restoreRecoveryArgs := append([]string{"restore", "--output", zipRecoveryPath, "--recovery-key", mnemonic}, dummyStoreArgs...)
				run(t, bin, restoreRecoveryArgs...)

				if got := readZipFile(t, zipRecoveryPath, "file1.txt"); got != "hello world" {
					t.Errorf("Recovery Restore content mismatch for file1.txt: got %q, want %q", got, "hello world")
				}

				// 14. Test Forget Policy & Dry Run
				// Re-init another dummy store to test forget logic
				dummyPolicyDir := t.TempDir()
				dummyPolicyStoreArgs := []string{"--store", "local", "--store-path", dummyPolicyDir}
				run(t, bin, append([]string{"init", "--encryption-password", password}, dummyPolicyStoreArgs...)...)

				for i := range 3 {
					src.WriteFile(t, "policy-file.txt", strings.Repeat("x", i+1))
					run(t, bin, append([]string{"backup", "--encryption-password", password}, append(srcArgs, dummyPolicyStoreArgs...)...)...)
				}

				out = run(t, bin, append([]string{"forget", "--keep-last", "1", "--dry-run", "--encryption-password", password}, dummyPolicyStoreArgs...)...)
				if !strings.Contains(out, "would remove") {
					t.Errorf("Expected dry-run output, got: %s", out)
				}

				run(t, bin, append([]string{"forget", "--keep-last", "1", "--prune", "--encryption-password", password}, dummyPolicyStoreArgs...)...)
				out = run(t, bin, append([]string{"list", "--encryption-password", password}, dummyPolicyStoreArgs...)...)
				if !strings.Contains(out, "1 snapshot") {
					t.Errorf("Expected 1 snapshot after policy, got: %s", out)
				}

				// 15. Test Unencrypted Backup Lifecycle
				// Verify that the full backup/restore flow works without encryption.
				unencDir := t.TempDir()
				unencStoreArgs := []string{"--store", "local", "--store-path", unencDir}

				// 15a. Init with --no-encryption
				out = run(t, bin, append([]string{"init", "--no-encryption"}, unencStoreArgs...)...)
				if !strings.Contains(out, "encrypted: false") {
					t.Errorf("Expected 'encrypted: false' in init output, got: %s", out)
				}

				// 15b. Backup without any encryption flags
				run(t, bin, append([]string{"backup"}, append(srcArgs, unencStoreArgs...)...)...)

				// 15c. List — verify 1 snapshot
				out = run(t, bin, append([]string{"list"}, unencStoreArgs...)...)
				if !strings.Contains(out, "1 snapshot") {
					t.Fatalf("Unencrypted: expected 1 snapshot, got: %s", out)
				}

				// 15d. Incremental backup
				src.WriteFile(t, "unenc-file.txt", "plaintext content")
				run(t, bin, append([]string{"backup"}, append(srcArgs, unencStoreArgs...)...)...)

				out = run(t, bin, append([]string{"list"}, unencStoreArgs...)...)
				if !strings.Contains(out, "2 snapshots") {
					t.Fatalf("Unencrypted: expected 2 snapshots, got: %s", out)
				}

				// 15e. Restore — verify file contents round-trip
				unencZipPath := filepath.Join(restoreDir, "unenc_restore.zip")
				run(t, bin, append([]string{"restore", "--output", unencZipPath}, unencStoreArgs...)...)

				for _, tc := range []struct {
					path    string
					content string
				}{
					{"file1.txt", "hello world"},
					{"secret.txt", "updated classified data"},
					{"subdir/nested.txt", "nested content"},
					{"unenc-file.txt", "plaintext content"},
				} {
					if got := readZipFile(t, unencZipPath, tc.path); got != tc.content {
						t.Errorf("Unencrypted restore mismatch for %s: got %q, want %q", tc.path, got, tc.content)
					}
				}

				// 15e2. Check unencrypted repository integrity
				out = run(t, bin, append([]string{"check", "--read-data"}, unencStoreArgs...)...)
				if !strings.Contains(out, "repository is healthy") {
					t.Errorf("Unencrypted: expected healthy check output, got: %s", out)
				}

				// 15f. Forget + Prune — verify cleanup works without encryption
				out = run(t, bin, append([]string{"forget", "--keep-last", "1", "--prune"}, unencStoreArgs...)...)
				if !strings.Contains(out, "Objects deleted:") {
					t.Errorf("Unencrypted: expected prune to delete objects, got: %s", out)
				}
				if !strings.Contains(out, "Space reclaimed:") {
					t.Errorf("Unencrypted: expected prune to reclaim space, got: %s", out)
				}

				out = run(t, bin, append([]string{"list"}, unencStoreArgs...)...)
				if !strings.Contains(out, "1 snapshot") {
					t.Errorf("Unencrypted: expected 1 snapshot after prune, got: %s", out)
				}
			})
		}
	}
}

// readZipFile reads a single file's content from a zip archive.
func readZipFile(t *testing.T, zipPath, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open zip entry %s: %v", name, err)
			}
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read zip entry %s: %v", name, err)
			}
			return string(data)
		}
	}
	t.Fatalf("file %s not found in zip %s", name, zipPath)
	return ""
}

// assertZipMissing verifies that a file is NOT present in a zip archive.
func assertZipMissing(t *testing.T, zipPath, name string) {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name == name {
			t.Errorf("file %s should NOT be present in zip %s", name, zipPath)
			return
		}
	}
}

// extractMnemonic pulls the 24-word BIP39 mnemonic from the recovery key
// box printed to stderr. The mnemonic line starts with "║  " and contains
// at least 20 space-separated words.
func extractMnemonic(t *testing.T, output string) string {
	t.Helper()
	re := regexp.MustCompile(`║\s{2}((?:\w+\s+){23}\w+)`)
	m := re.FindStringSubmatch(output)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}
