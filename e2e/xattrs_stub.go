//go:build !linux && !darwin

package e2e

import "testing"

func maybeSetTestXattr(t *testing.T, src TestSource, relPath, value string) (string, bool) {
	t.Helper()
	return "", false
}

func assertXattrValue(t *testing.T, path, name, want string) {
	t.Helper()
	t.Skip("xattr validation not supported on this platform")
}
