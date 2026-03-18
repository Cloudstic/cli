//go:build linux || darwin

package engine

import (
	"errors"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"golang.org/x/sys/unix"
)

var setRestoreXattr = unix.Setxattr

func applyRestoreXattrs(path string, meta core.FileMeta, warn func(string, ...interface{})) error {
	for name, value := range meta.Xattrs {
		if shouldSkipRestoredXattr(name) {
			continue
		}
		if err := setRestoreXattr(path, name, value, 0); err != nil {
			if isRestoreXattrBestEffortError(err) {
				if warn != nil {
					warn("could not set xattr %q: %v", name, err)
				}
				continue
			}
			return fmt.Errorf("set xattr %q on %s: %w", name, path, err)
		}
	}
	return nil
}

func isRestoreXattrBestEffortError(err error) bool {
	return errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EACCES)
}

func shouldSkipRestoredXattr(name string) bool {
	switch name {
	case "com.apple.provenance":
		return true
	default:
		return false
	}
}
