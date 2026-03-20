package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLI_Feature_ProfilesLocalStore verifies that the 'store new', 'profile new',
// and 'backup -profile' / 'backup -all-profiles' commands work end-to-end with a
// local store. Profiles are store-level configuration; testing with local store
// provides sufficient coverage.
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

	// Create a named store entry in profiles.yaml using a secret reference.
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

	// Create two backup profiles attached to the same store.
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

	// Initialise the repository with a plain password (matches the env secret).
	run(t, bin, "init", "--store", "local:"+storeDir, "--password", password)

	// Backup a single profile.
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

	// Backup all profiles.
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
