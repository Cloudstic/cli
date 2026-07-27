package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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

// TestCLI_Feature_ProfilesAllProfilesJSON verifies that 'backup -all-profiles
// -json' keeps stdout as a clean stream of JSON documents (no "== Running
// profile" banner interleaved), and that a per-profile merge failure is
// reported as a JSON error on stderr rather than plain text, consistent with
// every other command's -json contract.
func TestCLI_Feature_ProfilesAllProfilesJSON(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)
	src1 := t.TempDir()
	storeDir := t.TempDir()
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")

	writeFile(t, src1, "alpha.txt", "from profile one")

	passwordEnv := "E2E_PROFILE_PASSWORD"
	password := "e2e-profile-pass"

	run(t, bin,
		"store", "new",
		"-profiles-file", profilesPath,
		"-name", "main",
		"-uri", "local:"+storeDir,
		"-password-secret", "env://"+passwordEnv,
	)
	// "temp" only exists long enough for "profile new" to accept it as a
	// valid store-ref; it's then deleted below to leave "broken" with a
	// dangling reference, the same state a user reaches by removing a store
	// entry a profile still points at.
	run(t, bin,
		"store", "new",
		"-profiles-file", profilesPath,
		"-name", "temp",
		"-uri", "local:"+t.TempDir(),
		"-password-secret", "env://"+passwordEnv,
	)
	run(t, bin,
		"profile", "new",
		"-profiles-file", profilesPath,
		"-name", "ok",
		"-source", "local:"+src1,
		"-store-ref", "main",
	)
	run(t, bin,
		"profile", "new",
		"-profiles-file", profilesPath,
		"-name", "broken",
		"-source", "local:"+src1,
		"-store-ref", "temp",
	)
	removeYAMLStoreBlock(t, profilesPath, "temp")

	run(t, bin, "init", "--store", "local:"+storeDir, "--password", password)

	cmd := exec.Command(bin,
		"backup",
		"-profiles-file", profilesPath,
		"-all-profiles",
		"-json",
	)
	cmd.Env = append(cleanEnv(), passwordEnv+"="+password)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected backup -all-profiles -json to fail (the \"broken\" profile has an unknown store), succeeded instead.\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	if strings.Contains(stdout.String(), "== Running profile") {
		t.Fatalf("stdout leaked the human-readable profile banner under -json:\n%s", stdout.String())
	}

	// stdout must be exclusively JSON documents (the "ok" profile's backup
	// result) -- no stray plain text interleaved.
	dec := json.NewDecoder(&stdout)
	sawResult := false
	for dec.More() {
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("stdout is not a clean JSON stream: %v\nremaining stdout:\n%s", err, stdout.String())
		}
		sawResult = true
	}
	if !sawResult {
		t.Fatalf("expected at least one JSON result on stdout from the successful profile, got none")
	}

	// stderr must report the merge failure as a JSON error, not raw text.
	if strings.Contains(stderr.String(), "profile merge failed") && !strings.Contains(stderr.String(), `"error"`) {
		t.Fatalf("stderr has a bare (non-JSON) merge-failure message under -json:\n%s", stderr.String())
	}
	stderrDec := json.NewDecoder(&stderr)
	sawMergeError := false
	for stderrDec.More() {
		var e struct {
			Error string `json:"error"`
		}
		if err := stderrDec.Decode(&e); err != nil {
			t.Fatalf("stderr is not a clean JSON stream: %v\nremaining stderr:\n%s", err, stderr.String())
		}
		if strings.Contains(e.Error, "broken") && strings.Contains(e.Error, "profile merge failed") {
			sawMergeError = true
		}
	}
	if !sawMergeError {
		t.Fatalf("expected a JSON error mentioning the \"broken\" profile's merge failure on stderr, got:\n%s", stderr.String())
	}
}

// removeYAMLStoreBlock deletes the named entry (and its indented body) from
// the "stores:" mapping in profilesPath, leaving any profile that referenced
// it with a dangling store-ref.
func removeYAMLStoreBlock(t *testing.T, profilesPath, name string) {
	t.Helper()
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	header := "    " + name + ":"
	out := make([]string, 0, len(lines))
	skipping := false
	removed := false
	for _, line := range lines {
		if line == header {
			skipping = true
			removed = true
			continue
		}
		if skipping {
			if strings.HasPrefix(line, "        ") {
				continue
			}
			skipping = false
		}
		out = append(out, line)
	}
	if !removed {
		t.Fatalf("store entry %q not found in profiles file:\n%s", name, raw)
	}
	if err := os.WriteFile(profilesPath, []byte(strings.Join(out, "\n")), 0o600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}
}
