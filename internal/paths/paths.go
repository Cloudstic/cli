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
//  1. CLOUDSTIC_CONFIG_DIR environment variable (if set)
//  2. os.UserConfigDir()/cloudstic  (platform default)
//
// The directory is created with 0700 permissions if it does not exist.
func ConfigDir() (string, error) {
	if dir := os.Getenv("CLOUDSTIC_CONFIG_DIR"); dir != "" {
		return ensureDir(dir)
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determine config directory: %w", err)
	}
	return ensureDir(filepath.Join(base, appName))
}

// TokenPath returns the full path for a token file stored inside the config
// directory (e.g. "google_token.json" → "~/.config/cloudstic/google_token.json").
func TokenPath(filename string) (string, error) {
	dir, err := ConfigDir()
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

func ensureDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create config directory %s: %w", dir, err)
	}
	return dir, nil
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
