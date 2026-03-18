package e2e

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMain sets up a shared GOCOVERDIR so that every instrumented binary
// spawned during the e2e suite writes its coverage data to a single directory.
// After all tests complete, the binary coverage data is converted to the
// standard textfmt profile (compatible with go tool cover / Codecov) and
// written to the path in E2E_COVERAGE_OUT, if set.
func TestMain(m *testing.M) {
	// Respect an externally-provided GOCOVERDIR (e.g. for custom CI setups).
	// Otherwise create a temporary directory and own its lifecycle.
	coverDir := os.Getenv("GOCOVERDIR")
	ownsDir := coverDir == ""
	if ownsDir {
		var err error
		coverDir, err = os.MkdirTemp("", "e2e-cover-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e: failed to create GOCOVERDIR: %v\n", err)
			os.Exit(1)
		}
		if err := os.Setenv("GOCOVERDIR", coverDir); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: failed to set GOCOVERDIR: %v\n", err)
			os.Exit(1)
		}
	}

	code := m.Run()

	// Convert binary coverage data → textfmt so it can be merged with the
	// unit-test profile and uploaded to Codecov.
	if outFile := os.Getenv("E2E_COVERAGE_OUT"); outFile != "" {
		cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+coverDir, "-o="+outFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: coverage conversion failed: %v\n%s\n", err, out)
		}
	}

	if ownsDir {
		_ = os.RemoveAll(coverDir)
	}

	os.Exit(code)
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
	cmd := exec.Command("go", "build", "-cover", "-o", bin, "../cmd/cloudstic")
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

func runWithEnv(t *testing.T, bin string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(cleanEnv(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Command %v failed: %v\n%s", args, err, out)
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
// Matrix E2E Test Suite
// ----------------------------------------------------------------------------

func TestCLI_EndToEnd_Matrix(t *testing.T) {
	bin := buildBinary(t)

	// In the future, we will selectively append Google Drive, OneDrive, S3, B2, etc
	// here depending on the environment and available secrets.
	sources := []func(t *testing.T) TestSource{
		func(t *testing.T) TestSource { return newLocalSource(t) },
	}
	sources = append(sources, portableDriveSources()...)
	stores := []func(t *testing.T) TestStore{
		func(t *testing.T) TestStore { return newLocalStore(t) },
	}

	// Only add Docker-based hermetic tests if Docker is actually available
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err == nil {
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
				baseEncArgs := append(storeArgs, "-password", password)

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
				xattrName, hasXattrValidation := maybeSetTestXattr(t, src, "secret.txt", "classified-xattr")

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

				// 8.1 Restore Latest directly to filesystem directory
				dirOut := filepath.Join(restoreDir, "restore-dir")
				restoreDirArgs := append([]string{"restore"}, baseEncArgs...)
				restoreDirArgs = append(restoreDirArgs, "-format", "dir", "-output", dirOut)
				run(t, bin, restoreDirArgs...)

				for _, tc := range []struct {
					path    string
					content string
				}{
					{"file1.txt", "hello world"},
					{"file2.txt", "new file"},
					{"secret.txt", "updated classified data"},
					{"subdir/nested.txt", "nested content"},
				} {
					contentPath := filepath.Join(dirOut, filepath.FromSlash(tc.path))
					b, err := os.ReadFile(contentPath)
					if err != nil {
						t.Errorf("Direct restore missing %s: %v", tc.path, err)
						continue
					}
					if got := string(b); got != tc.content {
						t.Errorf("Direct restore content mismatch for %s: got %q, want %q", tc.path, got, tc.content)
					}
				}
				if hasXattrValidation {
					assertXattrValue(t, filepath.Join(dirOut, "secret.txt"), xattrName, "classified-xattr")
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

				// 8c. Partial Restore (dir format) — single file
				partialDirOut := filepath.Join(restoreDir, "partial-dir")
				partialDirArgs := append([]string{"restore"}, baseEncArgs...)
				partialDirArgs = append(partialDirArgs, "-format", "dir", "-output", partialDirOut, "-path", "file1.txt")
				run(t, bin, partialDirArgs...)

				b, err := os.ReadFile(filepath.Join(partialDirOut, "file1.txt"))
				if err != nil {
					t.Fatalf("partial dir restore missing file1.txt: %v", err)
				}
				if got := string(b); got != "hello world" {
					t.Errorf("Partial dir restore mismatch: got %q, want %q", got, "hello world")
				}
				if _, err := os.Stat(filepath.Join(partialDirOut, "file2.txt")); err == nil {
					t.Errorf("file2.txt should not be present in partial dir restore")
				}

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
				out = runExpectFail(t, bin, append([]string{"list", "--password", "wrong-password"}, storeArgs...)...)
				if !strings.Contains(out, "no provided credential matches") {
					t.Errorf("Expected credential mismatch error, got: %s", out)
				}

				// 11. Test Init Requires Encryption
				// (We use a fresh initialized dummy temp dir for this test to not ruin the matrix store)
				dummyStoreDir := t.TempDir()
				dummyStoreArgs := []string{"--store", "local:" + dummyStoreDir}
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
				out = run(t, bin, append([]string{"init", "--adopt-slots", "--password", password, "--add-recovery-key"}, dummyStoreArgs...)...)
				if !strings.Contains(out, "RECOVERY KEY") {
					t.Fatalf("Expected recovery key output on init, got: %s", out)
				}
				mnemonic := extractMnemonic(t, out)
				if mnemonic == "" {
					t.Fatal("Could not extract mnemonic from recovery key output")
				}

				run(t, bin, append([]string{"backup", "--password", password}, append(srcArgs, dummyStoreArgs...)...)...)

				zipRecoveryPath := filepath.Join(restoreDir, "recovery_restore.zip")
				restoreRecoveryArgs := append([]string{"restore", "--output", zipRecoveryPath, "--recovery-key", mnemonic}, dummyStoreArgs...)
				run(t, bin, restoreRecoveryArgs...)

				if got := readZipFile(t, zipRecoveryPath, "file1.txt"); got != "hello world" {
					t.Errorf("Recovery Restore content mismatch for file1.txt: got %q, want %q", got, "hello world")
				}

				// 14. Test Forget Policy & Dry Run
				// Re-init another dummy store to test forget logic
				dummyPolicyDir := t.TempDir()
				dummyPolicyStoreArgs := []string{"--store", "local:" + dummyPolicyDir}
				run(t, bin, append([]string{"init", "--password", password}, dummyPolicyStoreArgs...)...)

				for i := range 3 {
					src.WriteFile(t, "policy-file.txt", strings.Repeat("x", i+1))
					run(t, bin, append([]string{"backup", "--password", password}, append(srcArgs, dummyPolicyStoreArgs...)...)...)
				}

				out = run(t, bin, append([]string{"forget", "--keep-last", "1", "--dry-run", "--password", password}, dummyPolicyStoreArgs...)...)
				if !strings.Contains(out, "would remove") {
					t.Errorf("Expected dry-run output, got: %s", out)
				}

				run(t, bin, append([]string{"forget", "--keep-last", "1", "--prune", "--password", password}, dummyPolicyStoreArgs...)...)
				out = run(t, bin, append([]string{"list", "--password", password}, dummyPolicyStoreArgs...)...)
				if !strings.Contains(out, "1 snapshot") {
					t.Errorf("Expected 1 snapshot after policy, got: %s", out)
				}

				// 15. Test Unencrypted Backup Lifecycle
				// Verify that the full backup/restore flow works without encryption.
				unencDir := t.TempDir()
				unencStoreArgs := []string{"--store", "local:" + unencDir}

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

// TestCLI_EndToEnd_BackupExcludePatterns tests -exclude and -exclude-file flags.
func TestCLI_EndToEnd_BackupExcludePatterns(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)
	srcDir := t.TempDir()
	storeDir := t.TempDir()
	restoreDir := t.TempDir()

	// Create source files.
	for _, d := range []string{"src", ".git/objects", "node_modules/pkg", "logs"} {
		writeFile(t, srcDir, filepath.Join(d, ".keep"), "")
	}
	writeFile(t, srcDir, "src/main.go", "package main")
	writeFile(t, srcDir, "src/debug.log", "log line")
	writeFile(t, srcDir, ".git/config", "[core]")
	writeFile(t, srcDir, ".git/objects/abc", "blob")
	writeFile(t, srcDir, "node_modules/pkg/index.js", "exports")
	writeFile(t, srcDir, "logs/app.log", "log")
	writeFile(t, srcDir, "README.md", "hello")
	writeFile(t, srcDir, "notes.tmp", "temp")

	// Create an exclude file.
	excludeFilePath := filepath.Join(srcDir, ".backupignore")
	if err := os.WriteFile(excludeFilePath, []byte("# Skip logs dir\nlogs/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	password := "test-exclude-pass"
	storeArgs := []string{"--store", "local:" + storeDir}
	baseArgs := append(storeArgs, "--password", password)

	// Init repo.
	run(t, bin, append([]string{"init"}, baseArgs...)...)

	// Backup with -exclude flags and -exclude-file.
	backupArgs := append([]string{"backup",
		"-source", "local:" + srcDir,
		"-exclude", ".git/",
		"-exclude", "node_modules/",
		"-exclude", "*.tmp",
		"-exclude", "*.log",
		"-exclude-file", excludeFilePath,
	}, baseArgs...)
	run(t, bin, backupArgs...)

	// Restore and verify.
	zipPath := filepath.Join(restoreDir, "exclude_restore.zip")
	restoreArgs := append([]string{"restore", "--output", zipPath}, baseArgs...)
	run(t, bin, restoreArgs...)

	// Files that SHOULD be in the restore.
	for _, f := range []string{"src/main.go", "README.md"} {
		if !zipFileExists(t, zipPath, f) {
			t.Errorf("expected %q in restore archive", f)
		}
	}

	// Files that should NOT be in the restore.
	for _, f := range []string{
		".git/config", ".git/objects/abc",
		"node_modules/pkg/index.js",
		"notes.tmp", "src/debug.log", "logs/app.log",
	} {
		if zipFileExists(t, zipPath, f) {
			t.Errorf("excluded file %q should NOT be in restore archive", f)
		}
	}

	// Verify included file contents.
	if got := readZipFile(t, zipPath, "src/main.go"); got != "package main" {
		t.Errorf("src/main.go content = %q, want %q", got, "package main")
	}
	if got := readZipFile(t, zipPath, "README.md"); got != "hello" {
		t.Errorf("README.md content = %q, want %q", got, "hello")
	}
}

func TestCLI_EndToEnd_Completion(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)

	// Test each shell
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			out := run(t, bin, "completion", shell)
			if out == "" {
				t.Fatalf("completion %s produced empty output", shell)
			}
			// Verify it contains the command name at minimum
			if !strings.Contains(out, "cloudstic") {
				t.Errorf("completion %s output missing 'cloudstic'", shell)
			}
		})
	}

	// Test unsupported shell
	out := runExpectFail(t, bin, "completion", "powershell")
	if !strings.Contains(out, "Unsupported shell") {
		t.Errorf("Expected unsupported shell error, got: %s", out)
	}

	// Test no shell argument
	out = runExpectFail(t, bin, "completion")
	if !strings.Contains(out, "Usage:") {
		t.Errorf("Expected usage message, got: %s", out)
	}
}

func TestCLI_EndToEnd_Profiles_LocalStore(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)
	src1 := t.TempDir()
	src2 := t.TempDir()
	storeDir := t.TempDir()
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")

	writeFile(t, src1, "alpha.txt", "from profile one")
	writeFile(t, src2, "beta.txt", "from profile two")

	passwordEnv := "E2E_PROFILE_PASSWORD"
	password := "e2e-profile-pass"

	// Create store and ensure the new secret-ref format is written.
	run(t, bin,
		"store", "new",
		"-profiles-file", profilesPath,
		"-name", "main",
		"-uri", "local:"+storeDir,
		"-password-secret", "env://"+passwordEnv,
	)

	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	profilesYAML := string(raw)
	if !strings.Contains(profilesYAML, "password_secret: env://"+passwordEnv) {
		t.Fatalf("expected password_secret env ref in profiles file:\n%s", profilesYAML)
	}
	if strings.Contains(profilesYAML, "password_env:") {
		t.Fatalf("did not expect legacy password_env in profiles file:\n%s", profilesYAML)
	}

	// Create two local backup profiles using the shared store.
	run(t, bin,
		"profile", "new",
		"-profiles-file", profilesPath,
		"-name", "p1",
		"-source", "local:"+src1,
		"-store-ref", "main",
	)
	run(t, bin,
		"profile", "new",
		"-profiles-file", profilesPath,
		"-name", "p2",
		"-source", "local:"+src2,
		"-store-ref", "main",
	)

	// Initialize repository in the referenced store.
	run(t, bin, "init", "--store", "local:"+storeDir, "--password", password)

	// Run one profile; password is resolved from password_secret -> env://.
	runWithEnv(t, bin, []string{passwordEnv + "=" + password},
		"backup",
		"-profiles-file", profilesPath,
		"-profile", "p1",
	)

	out := run(t, bin, "list", "--store", "local:"+storeDir, "--password", password)
	if !strings.Contains(out, "1 snapshot") {
		t.Fatalf("expected one snapshot after single-profile backup, got:\n%s", out)
	}
	if !strings.Contains(out, src1) {
		t.Fatalf("expected source path for p1 in list output, got:\n%s", out)
	}

	// Run all profiles; should cover both profile entries end-to-end.
	runWithEnv(t, bin, []string{passwordEnv + "=" + password},
		"backup",
		"-profiles-file", profilesPath,
		"-all-profiles",
	)

	out = run(t, bin, "list", "--store", "local:"+storeDir, "--password", password)
	if !strings.Contains(out, src1) || !strings.Contains(out, src2) {
		t.Fatalf("expected both profile sources in list output after -all-profiles, got:\n%s", out)
	}
}

// zipFileExists checks if a file exists in the zip archive.
func zipFileExists(t *testing.T, zipPath, name string) bool {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name == name {
			return true
		}
	}
	return false
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
