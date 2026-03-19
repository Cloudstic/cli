package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
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
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("auth new failed: %s", errOut.String())
	}

	os.Args = []string{"cloudstic", "auth", "list", "-profiles-file", profilesPath}
	out.Reset()
	errOut.Reset()
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("auth list failed: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "Auth") || !strings.Contains(out.String(), "google-work") || !strings.Contains(out.String(), "PROVIDER") {
		t.Fatalf("unexpected auth list output:\n%s", out.String())
	}

	os.Args = []string{"cloudstic", "auth", "show", "-profiles-file", profilesPath, "google-work"}
	out.Reset()
	errOut.Reset()
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("auth show failed: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "Auth google-work") || !strings.Contains(out.String(), "Provider Details") || !strings.Contains(out.String(), "/tmp/google-work.json") {
		t.Fatalf("unexpected auth show output:\n%s", out.String())
	}
}

func TestRunAuth_NoSubcommand(t *testing.T) {
	os.Args = []string{"cloudstic", "auth"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runAuth(context.Background()); code != 1 {
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
	if code := r.runAuth(context.Background()); code != 1 {
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
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("auth new failed: %s", errOut.String())
	}

	// Verify via show
	os.Args = []string{"cloudstic", "auth", "show", "-profiles-file", profilesPath, "od-personal"}
	out.Reset()
	errOut.Reset()
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("auth show failed: %s", errOut.String())
	}
	got := out.String()
	for _, want := range []string{"Auth od-personal", "Provider Details", "/tmp/od-personal.json", "my-client-id-123"} {
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
	if code := r.runAuth(context.Background()); code != 1 {
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
	if code := r.runAuth(context.Background()); code != 1 {
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
	if code := r.runAuth(context.Background()); code != 1 {
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
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("setup auth new failed: %s", errOut.String())
	}

	// Try to show a non-existent auth entry
	os.Args = []string{"cloudstic", "auth", "show", "-profiles-file", profilesPath, "missing"}
	out.Reset()
	errOut.Reset()
	if code := r.runAuth(context.Background()); code != 1 {
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
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("expected exit code 0, got %d; errOut: %s", code, errOut.String())
	}
}

func TestDefaultAuthTokenRef(t *testing.T) {
	if got := defaultAuthTokenRef("google", "work"); got != "config-token://google/work" {
		t.Fatalf("unexpected ref: %q", got)
	}
	if got := defaultAuthTokenRef("google", ""); got != "config-token://google/default" {
		t.Fatalf("unexpected default ref for empty name: %q", got)
	}
}

func TestRunAuthNew_OneDriveDerivesTokenRef(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{
		"cloudstic", "auth", "new",
		"-profiles-file", profilesPath,
		"-name", "od-work",
		"-provider", "onedrive",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, noPrompt: true}
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("auth new failed: %s", errOut.String())
	}

	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	if !strings.Contains(string(raw), "onedrive_token_ref: config-token://onedrive/od-work") {
		t.Fatalf("expected derived onedrive token ref in profiles file:\n%s", string(raw))
	}
}

func TestRunAuthNew_DerivesDefaultTokenRef(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{"cloudstic", "auth", "new", "-profiles-file", profilesPath, "-name", "g", "-provider", "google"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, noPrompt: true}
	if code := r.runAuth(context.Background()); code != 0 {
		t.Fatalf("auth new failed: %s", errOut.String())
	}
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	if !strings.Contains(string(raw), "google_token_ref: config-token://google/g") {
		t.Fatalf("expected derived token ref in profiles file:\n%s", string(raw))
	}
}

func TestPromptAuthSelection_DerivesTokenRef(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmpDir)
	cfg := &cloudstic.ProfilesConfig{
		Auth: make(map[string]cloudstic.ProfileAuth),
	}

	r := &runner{
		out:    &strings.Builder{},
		errOut: &strings.Builder{},
		// Mock prompt interactions:
		// 1. Select option: "Create new auth"
		// 2. Auth name: "my-google"
		// 3. Token storage: use default (config-token://google/my-google)
		lineIn: bufio.NewReader(strings.NewReader("1\nmy-google\n\n")),
	}

	ctx := context.Background()
	name, code := r.promptAuthSelection(ctx, cfg, "google", "test-profile")
	if code != 0 {
		t.Fatalf("promptAuthSelection failed with code %d", code)
	}
	if name != "my-google" {
		t.Fatalf("expected name 'my-google', got %q", name)
	}

	auth, ok := cfg.Auth["my-google"]
	if !ok {
		t.Fatal("auth entry not created in config")
	}
	expectedRef := "config-token://google/my-google"
	if auth.GoogleTokenRef != expectedRef {
		t.Fatalf("expected token ref %q, got %q", expectedRef, auth.GoogleTokenRef)
	}
}
