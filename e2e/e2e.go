package e2e

import (
	"os"
	"strings"
	"testing"
)

// TestEnv classifies a test integration environment.
type TestEnv string

const (
	// Hermetic runs entirely local (e.g., TempDir, Testcontainers). Safe for all machines.
	Hermetic TestEnv = "hermetic"
	// Live runs against real cloud vendor APIs (e.g., real AWS S3, real Google Drive). Requires secrets.
	Live TestEnv = "live"
)

// currentE2EMode returns the current test mode ("hermetic", "live", "all").
// Defaults to hermetic if unset.
func currentE2EMode() string {
	if mode := os.Getenv("CLOUDSTIC_E2E_MODE"); mode != "" {
		return strings.ToLower(mode)
	}
	return "hermetic"
}

// ----------------------------------------------------------------------------
// E2E Testing Matrix Interfaces
// ----------------------------------------------------------------------------

// TestSource encapsulates the origin of data to be backed up.
type TestSource interface {
	Name() string
	Env() TestEnv
	Setup(t *testing.T) (sourceArgs []string)
	WriteFile(t *testing.T, relPath, content string)
}

// TestStore encapsulates the content-addressable storage backend.
type TestStore interface {
	Name() string
	Env() TestEnv
	Setup(t *testing.T) (storeArgs []string)
}
