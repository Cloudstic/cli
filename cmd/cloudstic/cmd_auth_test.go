package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAuthNewAndListAndShow(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{
		"cloudstic", "auth", "new",
		"-profiles-file", profilesPath,
		"-name", "google-work",
		"-provider", "google",
		"-google-token-file", "/tmp/google-work.json",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 0 {
		t.Fatalf("auth new failed: %s", errOut.String())
	}

	os.Args = []string{"cloudstic", "auth", "list", "-profiles-file", profilesPath}
	out.Reset()
	errOut.Reset()
	if code := r.runAuth(); code != 0 {
		t.Fatalf("auth list failed: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "1 auth entries") || !strings.Contains(out.String(), "google-work") {
		t.Fatalf("unexpected auth list output:\n%s", out.String())
	}

	os.Args = []string{"cloudstic", "auth", "show", "-profiles-file", profilesPath, "google-work"}
	out.Reset()
	errOut.Reset()
	if code := r.runAuth(); code != 0 {
		t.Fatalf("auth show failed: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "provider: google") || !strings.Contains(out.String(), "google_token_file: /tmp/google-work.json") {
		t.Fatalf("unexpected auth show output:\n%s", out.String())
	}
}

func TestRunAuth_NoSubcommand(t *testing.T) {
	os.Args = []string{"cloudstic", "auth"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "Available subcommands") {
		t.Fatalf("unexpected errOut:\n%s", errOut.String())
	}
}

func TestRunAuth_UnknownSubcommand(t *testing.T) {
	os.Args = []string{"cloudstic", "auth", "unknown"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "Unknown auth subcommand") {
		t.Fatalf("unexpected errOut:\n%s", errOut.String())
	}
}

func TestRunAuthNew_OneDriveProvider(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{
		"cloudstic", "auth", "new",
		"-profiles-file", profilesPath,
		"-name", "od-personal",
		"-provider", "onedrive",
		"-onedrive-token-file", "/tmp/od-personal.json",
		"-onedrive-client-id", "my-client-id-123",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 0 {
		t.Fatalf("auth new failed: %s", errOut.String())
	}

	// Verify via show
	os.Args = []string{"cloudstic", "auth", "show", "-profiles-file", profilesPath, "od-personal"}
	out.Reset()
	errOut.Reset()
	if code := r.runAuth(); code != 0 {
		t.Fatalf("auth show failed: %s", errOut.String())
	}
	got := out.String()
	for _, want := range []string{"provider: onedrive", "onedrive_token_file: /tmp/od-personal.json", "onedrive_client_id: my-client-id-123"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in show output:\n%s", want, got)
		}
	}
}

func TestRunAuthNew_RequiresName(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{"cloudstic", "auth", "new", "-profiles-file", profilesPath, "-provider", "google"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, noPrompt: true}
	if code := r.runAuth(); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "-name is required") {
		t.Fatalf("unexpected errOut:\n%s", errOut.String())
	}
}

func TestRunAuthNew_RequiresProvider(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{"cloudstic", "auth", "new", "-profiles-file", profilesPath, "-name", "foo"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, noPrompt: true}
	if code := r.runAuth(); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "-provider must be") {
		t.Fatalf("unexpected errOut:\n%s", errOut.String())
	}
}

func TestRunAuthNew_InvalidName(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{"cloudstic", "auth", "new", "-profiles-file", profilesPath, "-name", "bad name!", "-provider", "google"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid auth name") {
		t.Fatalf("unexpected errOut:\n%s", errOut.String())
	}
}

func TestRunAuthShow_UnknownAuth(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	// Create a profiles file with one auth entry so the file exists
	os.Args = []string{
		"cloudstic", "auth", "new",
		"-profiles-file", profilesPath,
		"-name", "existing",
		"-provider", "google",
		"-google-token-file", "/tmp/tok.json",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 0 {
		t.Fatalf("setup auth new failed: %s", errOut.String())
	}

	// Try to show a non-existent auth entry
	os.Args = []string{"cloudstic", "auth", "show", "-profiles-file", profilesPath, "missing"}
	out.Reset()
	errOut.Reset()
	if code := r.runAuth(); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "Unknown auth") {
		t.Fatalf("unexpected errOut:\n%s", errOut.String())
	}
}

func TestRunAuthList_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "nonexistent.yaml")

	os.Args = []string{"cloudstic", "auth", "list", "-profiles-file", profilesPath}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 0 {
		t.Fatalf("expected exit code 0, got %d; errOut: %s", code, errOut.String())
	}
}

func TestDefaultAuthTokenPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmpDir)

	got := defaultAuthTokenPath("google", "work")
	want := filepath.Join("tokens", "google-work_token.json")
	if !strings.HasSuffix(got, want) {
		t.Fatalf("expected path ending with %q, got %q", want, got)
	}

	// Empty name should use "default"
	got = defaultAuthTokenPath("google", "")
	want = filepath.Join("tokens", "google-default_token.json")
	if !strings.HasSuffix(got, want) {
		t.Fatalf("expected path ending with %q, got %q", want, got)
	}
}

func TestRunAuthNew_OneDriveDerivesTokenFile(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmpDir)

	os.Args = []string{
		"cloudstic", "auth", "new",
		"-profiles-file", profilesPath,
		"-name", "od-work",
		"-provider", "onedrive",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 0 {
		t.Fatalf("auth new failed: %s", errOut.String())
	}

	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	if !strings.Contains(string(raw), "onedrive-od-work_token.json") {
		t.Fatalf("expected derived onedrive token file path in profiles file:\n%s", string(raw))
	}
}

func TestRunAuthNew_DerivesDefaultTokenFile(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmpDir)

	os.Args = []string{"cloudstic", "auth", "new", "-profiles-file", profilesPath, "-name", "g", "-provider", "google"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(); code != 0 {
		t.Fatalf("auth new failed: %s", errOut.String())
	}
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	if !strings.Contains(string(raw), "google-g_token.json") {
		t.Fatalf("expected derived token file path in profiles file:\n%s", string(raw))
	}
}
