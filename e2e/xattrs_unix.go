//go:build linux || darwin

package e2e

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func testXattrName() string {
	if runtimeGOOS() == "darwin" {
		return "com.cloudstic.e2e"
	}
	return "user.cloudstic.e2e"
}

func maybeSetTestXattr(t *testing.T, src TestSource, relPath, value string) (string, bool) {
	t.Helper()
	host, ok := src.(hostPathSource)
	if !ok {
		return "", false
	}
	name := testXattrName()
	fullPath := host.HostPath(filepath.FromSlash(relPath))
	if err := unix.Setxattr(fullPath, name, []byte(value), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Logf("skipping xattr validation for %s: cannot set %s on %s: %v", src.Name(), name, fullPath, err)
			return "", false
		}
		t.Fatalf("set xattr %s on %s: %v", name, fullPath, err)
	}
	return name, true
}

func assertXattrValue(t *testing.T, path, name, want string) {
	t.Helper()
	buf := make([]byte, 1024)
	n, err := unix.Getxattr(path, name, buf)
	if err != nil {
		t.Fatalf("get xattr %s on %s: %v", name, path, err)
	}
	if got := string(buf[:n]); got != want {
		t.Fatalf("xattr %s on %s = %q, want %q", name, path, got, want)
	}
}

func runtimeGOOS() string {
	return runtime.GOOS
}
