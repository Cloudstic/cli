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
	// (not always true in rudimentary CI environments)
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		stores = append(stores, func(t *testing.T) TestStore { return newMinIOTestStore(t) })
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

				// 9. Forget & Prune
				forgetArgs := append([]string{"forget", "--keep-last", "1", "--prune"}, baseEncArgs...)
				run(t, bin, forgetArgs...)

				out = run(t, bin, append([]string{"list"}, baseEncArgs...)...)
				if !strings.Contains(out, "1 snapshot") {
					t.Fatalf("Expected 1 snapshot after forget/prune, got: %s", out)
				}
			})
		}
	}
}

// ----------------------------------------------------------------------------
// Legacy Tests (Testing Specific CLI Features unrelated to the Matrix)
// ----------------------------------------------------------------------------

func TestCLI_InitRequiresEncryption(t *testing.T) {
	bin := buildBinary(t)
	storeDir := t.TempDir()

	out := runExpectFail(t, bin, "init", "--store", "local", "--store-path", storeDir)
	if !strings.Contains(out, "encryption is required") {
		t.Errorf("Expected encryption-required error, got: %s", out)
	}
}

func TestCLI_Encrypted_WrongPassword(t *testing.T) {
	bin := buildBinary(t)
	storeDir := t.TempDir()

	run(t, bin, "init",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", "correct-password")

	out := runExpectFail(t, bin, "list",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", "wrong-password")
	if !strings.Contains(out, "no provided credential matches") {
		t.Errorf("Expected credential mismatch error, got: %s", out)
	}
}

func TestCLI_Encrypted_NoPassword(t *testing.T) {
	bin := buildBinary(t)
	storeDir := t.TempDir()

	run(t, bin, "init",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", "my-password")

	out := runExpectFail(t, bin, "list",
		"--store", "local", "--store-path", storeDir)
	if !strings.Contains(out, "repository is encrypted") {
		t.Errorf("Expected encrypted-repo error, got: %s", out)
	}
}

func TestCLI_RecoveryKey(t *testing.T) {
	bin := buildBinary(t)
	srcDir := t.TempDir()
	storeDir := t.TempDir()
	restoreDir := t.TempDir()
	password := "recovery-test-password"

	writeFile(t, srcDir, "important.txt", "do not lose this")

	// Init with password + recovery key
	out := run(t, bin, "init",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password,
		"--recovery")
	if !strings.Contains(out, "RECOVERY KEY") {
		t.Fatalf("Expected recovery key output, got: %s", out)
	}

	// Extract the 24-word mnemonic from the output
	mnemonic := extractMnemonic(t, out)
	if mnemonic == "" {
		t.Fatal("Could not extract mnemonic from recovery key output")
	}
	words := strings.Fields(mnemonic)
	if len(words) != 24 {
		t.Fatalf("Expected 24-word mnemonic, got %d words: %q", len(words), mnemonic)
	}

	// Backup with password
	run(t, bin, "backup",
		"--source", "local", "--source-path", srcDir,
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)

	// Restore using recovery key (simulating lost password)
	zipPath := filepath.Join(restoreDir, "restore.zip")
	run(t, bin, "restore",
		"--store", "local", "--store-path", storeDir,
		"--recovery-key", mnemonic,
		"--output", zipPath)

	if got := readZipFile(t, zipPath, "important.txt"); got != "do not lose this" {
		t.Errorf("Content mismatch: got %q", got)
	}
}

func TestCLI_AddRecoveryKey(t *testing.T) {
	bin := buildBinary(t)
	srcDir := t.TempDir()
	storeDir := t.TempDir()
	restoreDir := t.TempDir()
	password := "add-recovery-test"

	writeFile(t, srcDir, "data.txt", "recovery test data")

	// Init without recovery
	run(t, bin, "init",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)

	// Backup
	run(t, bin, "backup",
		"--source", "local", "--source-path", srcDir,
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)

	// Add recovery key after the fact
	out := run(t, bin, "add-recovery-key",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)
	if !strings.Contains(out, "RECOVERY KEY") {
		t.Fatalf("Expected recovery key output, got: %s", out)
	}

	mnemonic := extractMnemonic(t, out)

	// Restore using the recovery key
	zipPath := filepath.Join(restoreDir, "restore.zip")
	run(t, bin, "restore",
		"--store", "local", "--store-path", storeDir,
		"--recovery-key", mnemonic,
		"--output", zipPath)

	if got := readZipFile(t, zipPath, "data.txt"); got != "recovery test data" {
		t.Errorf("Content mismatch: got %q", got)
	}
}

func TestCLI_ForgetPolicy_Encrypted(t *testing.T) {
	bin := buildBinary(t)
	srcDir := t.TempDir()
	storeDir := t.TempDir()
	password := "forget-policy-test"

	run(t, bin, "init",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)

	// Create 3 snapshots
	for i := range 3 {
		writeFile(t, srcDir, "file.txt", strings.Repeat("x", i+1))
		run(t, bin, "backup",
			"-source", "local", "-source-path", srcDir,
			"-store", "local", "-store-path", storeDir,
			"-encryption-password", password)
	}

	// Verify 3 snapshots
	out := run(t, bin, "list",
		"-store", "local", "-store-path", storeDir,
		"-encryption-password", password)
	if !strings.Contains(out, "3 snapshots") {
		t.Fatalf("Expected 3 snapshots: %s", out)
	}

	// Dry-run: keep last 1
	out = run(t, bin, "forget", "--keep-last", "1", "--dry-run",
		"-store", "local", "-store-path", storeDir,
		"-encryption-password", password)
	if !strings.Contains(out, "would remove") {
		t.Errorf("Expected dry-run output, got: %s", out)
	}

	// Apply: keep last 1 with prune
	run(t, bin, "forget", "--keep-last", "1", "--prune",
		"-store", "local", "-store-path", storeDir,
		"-encryption-password", password)

	out = run(t, bin, "list",
		"-store", "local", "-store-path", storeDir,
		"-encryption-password", password)
	if !strings.Contains(out, "1 snapshot") {
		t.Errorf("Expected 1 snapshot after policy, got: %s", out)
	}
}

// readZipFile reads a single file's content from a zip archive.
func readZipFile(t *testing.T, zipPath, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open zip entry %s: %v", name, err)
			}
			defer rc.Close()
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
