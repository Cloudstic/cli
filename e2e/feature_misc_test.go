package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLI_Feature_BackupExcludePatterns(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)
	srcDir := t.TempDir()
	storeDir := t.TempDir()
	restoreDir := t.TempDir()

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

	excludeFilePath := filepath.Join(srcDir, ".backupignore")
	if err := os.WriteFile(excludeFilePath, []byte("# Skip logs dir\nlogs/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	password := "test-exclude-pass"
	storeArgs := []string{"--store", "local:" + storeDir}
	baseArgs := append(storeArgs, "--password", password)

	run(t, bin, append([]string{"init"}, baseArgs...)...)

	backupArgs := append([]string{"backup",
		"-source", "local:" + srcDir,
		"-exclude", ".git/",
		"-exclude", "node_modules/",
		"-exclude", "*.tmp",
		"-exclude", "*.log",
		"-exclude-file", excludeFilePath,
	}, baseArgs...)
	run(t, bin, backupArgs...)

	zipPath := filepath.Join(restoreDir, "exclude_restore.zip")
	restoreArgs := append([]string{"restore", "--output", zipPath}, baseArgs...)
	run(t, bin, restoreArgs...)

	for _, f := range []string{"src/main.go", "README.md"} {
		if !zipFileExists(t, zipPath, f) {
			t.Errorf("expected %q in restore archive", f)
		}
	}

	for _, f := range []string{
		".git/config", ".git/objects/abc",
		"node_modules/pkg/index.js",
		"notes.tmp", "src/debug.log", "logs/app.log",
	} {
		if zipFileExists(t, zipPath, f) {
			t.Errorf("excluded file %q should not be in restore archive", f)
		}
	}

	if got := readZipFile(t, zipPath, "src/main.go"); got != "package main" {
		t.Errorf("src/main.go content = %q, want %q", got, "package main")
	}
	if got := readZipFile(t, zipPath, "README.md"); got != "hello" {
		t.Errorf("README.md content = %q, want %q", got, "hello")
	}
}

func TestCLI_Feature_Completion(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)

	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			out := run(t, bin, "completion", shell)
			if out == "" {
				t.Fatalf("completion %s produced empty output", shell)
			}
			if !strings.Contains(out, "cloudstic") {
				t.Errorf("completion %s output missing 'cloudstic'", shell)
			}
		})
	}

	out := runExpectFail(t, bin, "completion", "powershell")
	if !strings.Contains(out, "Unsupported shell") {
		t.Errorf("expected unsupported shell error, got: %s", out)
	}

	out = runExpectFail(t, bin, "completion")
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage message, got: %s", out)
	}
}

func TestCLI_Feature_ProfilesLocalStore(t *testing.T) {
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

	run(t, bin, "init", "--store", "local:"+storeDir, "--password", password)

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
