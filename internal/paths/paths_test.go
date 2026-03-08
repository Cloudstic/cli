package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDir_EnvVar(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "custom-config")
	t.Setenv("CLOUDSTIC_CONFIG_DIR", target)

	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != target {
		t.Errorf("got %q, want %q", dir, target)
	}
	// Dir should have been created.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Errorf("directory was not created")
	}
}

func TestConfigDir_Default(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", "")

	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir == "" {
		t.Error("expected non-empty directory")
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Errorf("directory %q does not exist", dir)
	}
}

func TestConfigDir_CreatesWithCorrectPermissions(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "new-dir")
	t.Setenv("CLOUDSTIC_CONFIG_DIR", target)

	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory")
	}
	// On Unix, permissions should be 0700.
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("expected 0700 permissions, got %o", perm)
	}
}

func TestTokenPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmp)

	path, err := TokenPath("google_token.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(tmp, "google_token.json")
	if path != expected {
		t.Errorf("got %q, want %q", path, expected)
	}
}

func TestTokenPath_ReturnsError(t *testing.T) {
	// Force an error by providing an invalid path that cannot be created.
	// We make the parent a file, not a directory.
	tmp := t.TempDir()
	file := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUDSTIC_CONFIG_DIR", filepath.Join(file, "subdir"))

	_, err := TokenPath("token.json")
	if err == nil {
		t.Error("expected error when config dir cannot be created")
	}
}
