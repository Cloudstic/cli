//go:build linux || darwin

package engine

import (
	"errors"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"golang.org/x/sys/unix"
)

var setRestoreXattr = unix.Setxattr

func applyRestoreXattrs(path string, meta core.FileMeta) error {
	for name, value := range meta.Xattrs {
		if err := setRestoreXattr(path, name, value, 0); err != nil {
			if isRestoreXattrBestEffortError(err) {
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
