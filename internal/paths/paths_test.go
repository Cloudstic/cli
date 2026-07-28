package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigDir_OverrideBeatsEnvVar(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "from-flag")
	t.Setenv("CLOUDSTIC_CONFIG_DIR", filepath.Join(tmp, "from-env"))

	dir, err := ConfigDir(target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != target {
		t.Errorf("got %q, want %q", dir, target)
	}
}

func TestConfigDir_EnvVar(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "custom-config")
	t.Setenv("CLOUDSTIC_CONFIG_DIR", target)

	dir, err := ConfigDir("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != target {
		t.Errorf("got %q, want %q", dir, target)
	}
}

func TestConfigDir_Default(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", "")

	dir, err := ConfigDir("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir on this platform: %v", err)
	}
	if want := filepath.Join(base, appName); dir != want {
		t.Errorf("got %q, want %q", dir, want)
	}
}

// Resolving a path must not create anything. Help output, shell completion and
// `setup -dry-run` all ask where configuration lives without intending to write
// there, and creating the directory to answer would be a side effect of asking.
func TestConfigDir_CreatesNothing(t *testing.T) {
	target := filepath.Join(t.TempDir(), "absent")

	for _, tc := range []struct {
		name string
		call func() (string, error)
	}{
		{"override", func() (string, error) { return ConfigDir(target) }},
		{"token path", func() (string, error) { return TokenPath(target, "google_token.json") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.call(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("directory %q was created, want untouched (stat err: %v)", target, err)
			}
		})
	}
}

func TestTokenPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmp)

	path, err := TokenPath("", "google_token.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(tmp, "google_token.json")
	if path != expected {
		t.Errorf("got %q, want %q", path, expected)
	}
}

// The config directory is created by whoever writes into it, at 0700, rather
// than by resolving its path. This is the guarantee that moved out of
// ConfigDir when resolution stopped touching the filesystem.
func TestSaveAtomic_CreatesConfigDirWithCorrectPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "new-dir")
	if err := SaveAtomic(filepath.Join(dir, "token.json"), []byte("x")); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory")
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("expected 0700 permissions, got %o", perm)
	}
}

func TestSaveAtomic_ReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveAtomic(path, []byte("new")); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("got %q want %q", got, "new")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat failed: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("got perms %o want 600", info.Mode().Perm())
		}
	}
}
