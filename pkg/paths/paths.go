package paths

import (
	"fmt"
	"os"
	"path/filepath"
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

func ensureDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create config directory %s: %w", dir, err)
	}
	return dir, nil
}
