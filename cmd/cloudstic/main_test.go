package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

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

func run(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Command %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func runExpectFail(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("Expected command %v to fail, but it succeeded:\n%s", args, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCLI_EndToEnd(t *testing.T) {
	bin := buildBinary(t)
	srcDir := t.TempDir()
	storeDir := t.TempDir()
	restoreDir := t.TempDir()

	writeFile(t, srcDir, "file1.txt", "hello world")

	// Init (unencrypted, requires explicit opt-out)
	run(t, bin, "init", "--store", "local", "--store-path", storeDir, "--no-encryption")

	// Backup
	run(t, bin, "backup",
		"--source", "local", "--source-path", srcDir,
		"--store", "local", "--store-path", storeDir)

	// List
	out := run(t, bin, "list", "--store", "local", "--store-path", storeDir)
	if !strings.Contains(out, "1") {
		t.Errorf("List output missing sequence 1: %s", out)
	}

	// Backup with tags
	writeFile(t, srcDir, "file2.txt", "new file")
	run(t, bin, "backup",
		"-source", "local", "-source-path", srcDir,
		"-store", "local", "-store-path", storeDir,
		"-tag", "daily", "-tag", "important")

	// List and check tags
	out = run(t, bin, "list", "-store", "local", "-store-path", storeDir)
	if !strings.Contains(out, "daily, important") {
		t.Errorf("List output missing tags 'daily, important': %s", out)
	}

	// Restore
	run(t, bin, "restore",
		"--store", "local", "--store-path", storeDir,
		"--target", restoreDir)

	data, err := os.ReadFile(filepath.Join(restoreDir, "file1.txt"))
	if err != nil {
		t.Fatalf("Restored file missing: %v", err)
	}
	if string(data) != "hello world" {
		t.Error("Content mismatch for file1.txt")
	}
}

func TestCLI_InitRequiresEncryption(t *testing.T) {
	bin := buildBinary(t)
	storeDir := t.TempDir()

	out := runExpectFail(t, bin, "init", "--store", "local", "--store-path", storeDir)
	if !strings.Contains(out, "encryption is required") {
		t.Errorf("Expected encryption-required error, got: %s", out)
	}
}

func TestCLI_EndToEnd_Encrypted(t *testing.T) {
	bin := buildBinary(t)
	srcDir := t.TempDir()
	storeDir := t.TempDir()
	restoreDir := t.TempDir()
	password := "test-passphrase-e2e"

	writeFile(t, srcDir, "secret.txt", "classified data")
	writeFile(t, srcDir, "notes.txt", "some notes")
	if err := os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, srcDir, "subdir/nested.txt", "nested content")

	// Init with password encryption
	out := run(t, bin, "init",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)
	if !strings.Contains(out, "encrypted: true") {
		t.Errorf("Expected encrypted repo, got: %s", out)
	}

	// Backup
	run(t, bin, "backup",
		"--source", "local", "--source-path", srcDir,
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)

	// List
	out = run(t, bin, "list",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)
	if !strings.Contains(out, "1") {
		t.Errorf("List output missing sequence 1: %s", out)
	}

	// Ls
	run(t, bin, "ls",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password)

	// Second backup with modifications
	writeFile(t, srcDir, "secret.txt", "updated classified data")
	writeFile(t, srcDir, "new-file.txt", "brand new")
	run(t, bin, "backup",
		"-source", "local", "-source-path", srcDir,
		"-store", "local", "-store-path", storeDir,
		"-encryption-password", password,
		"-tag", "v2")

	// List should show 2 snapshots
	out = run(t, bin, "list",
		"-store", "local", "-store-path", storeDir,
		"-encryption-password", password)
	if !strings.Contains(out, "2 snapshots") {
		t.Errorf("Expected 2 snapshots: %s", out)
	}

	// Restore latest
	run(t, bin, "restore",
		"--store", "local", "--store-path", storeDir,
		"--encryption-password", password,
		"--target", restoreDir)

	for _, tc := range []struct {
		path    string
		content string
	}{
		{"secret.txt", "updated classified data"},
		{"notes.txt", "some notes"},
		{"subdir/nested.txt", "nested content"},
		{"new-file.txt", "brand new"},
	} {
		data, err := os.ReadFile(filepath.Join(restoreDir, tc.path))
		if err != nil {
			t.Errorf("Restored file %s missing: %v", tc.path, err)
			continue
		}
		if string(data) != tc.content {
			t.Errorf("Content mismatch for %s: got %q, want %q", tc.path, data, tc.content)
		}
	}

	// Forget all but the latest snapshot and prune
	run(t, bin, "forget", "--keep-last", "1", "--prune",
		"-store", "local", "-store-path", storeDir,
		"-encryption-password", password)

	out = run(t, bin, "list",
		"-store", "local", "-store-path", storeDir,
		"-encryption-password", password)
	if !strings.Contains(out, "1 snapshot") {
		t.Errorf("Expected 1 snapshot after forget: %s", out)
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
	run(t, bin, "restore",
		"--store", "local", "--store-path", storeDir,
		"--recovery-key", mnemonic,
		"--target", restoreDir)

	data, err := os.ReadFile(filepath.Join(restoreDir, "important.txt"))
	if err != nil {
		t.Fatalf("Restored file missing: %v", err)
	}
	if string(data) != "do not lose this" {
		t.Errorf("Content mismatch: got %q", data)
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
	run(t, bin, "restore",
		"--store", "local", "--store-path", storeDir,
		"--recovery-key", mnemonic,
		"--target", restoreDir)

	data, err := os.ReadFile(filepath.Join(restoreDir, "data.txt"))
	if err != nil {
		t.Fatalf("Restored file missing: %v", err)
	}
	if string(data) != "recovery test data" {
		t.Errorf("Content mismatch: got %q", data)
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
