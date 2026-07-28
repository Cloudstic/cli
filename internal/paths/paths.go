package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const appName = "cloudstic"

// ConfigDir returns the directory for cloudstic configuration and state files.
// Resolution order:
//  1. override, when non-empty (the -config-dir flag)
//  2. CLOUDSTIC_CONFIG_DIR environment variable
//  3. os.UserConfigDir()/cloudstic  (platform default)
//
// Resolution performs no filesystem access. The directory is created, with
// 0700 permissions, by whoever writes into it — SaveAtomic and profile.Save
// both do — so asking where configuration *would* live never creates it. That
// matters because the answer is needed on paths that must not have side
// effects: rendering help, generating completions, and previewing a setup with
// -dry-run all resolve this path without any intent to write.
func ConfigDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if dir := os.Getenv("CLOUDSTIC_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determine config directory: %w", err)
	}
	return filepath.Join(base, appName), nil
}

// TokenPath returns the full path for a token file stored inside the config
// directory (e.g. "google_token.json" → "~/.config/cloudstic/google_token.json").
// override has ConfigDir's meaning.
func TokenPath(override, filename string) (string, error) {
	dir, err := ConfigDir(override)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}

// MachineID returns a unique identifier for the current machine.
// It tries to read from common system files.
func MachineID() string {
	// 1. Linux/BSD
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	// 2. Fallback to hostname if nothing else works
	host, _ := os.Hostname()
	return strings.TrimSpace(host)
}

// SaveAtomic writes data to a temporary file in the target directory and
// atomically renames it to path to prevent file corruption during crashes.
// It ensures 0600 permissions on the final file.
func SaveAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	// No need to Chmod as CreateTemp already creates 0600 on Unix systems.

	if _, err := tmp.Write(data); err != nil {
		return err
	}

	if err := tmp.Sync(); err != nil {
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return replaceFile(tmp.Name(), path)
}

func replaceFile(src, dst string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(src, dst)
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}
