//go:build linux || darwin

package engine

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/cloudstic/cli/internal/core"
)

func applyRestoreFileMetadata(path string, meta core.FileMeta, warn func(string, ...interface{})) error {
	if meta.Mode != 0 {
		if err := os.Chmod(path, fs.FileMode(meta.Mode)); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	if meta.Uid != 0 || meta.Gid != 0 {
		if err := os.Lchown(path, int(meta.Uid), int(meta.Gid)); err != nil && warn != nil {
			warn("could not set ownership on %s: %v", path, err)
		} else if err == nil && meta.Mode&0o6000 != 0 {
			_ = os.Chmod(path, fs.FileMode(meta.Mode))
		}
	}
	if err := applyRestoreTimes(path, meta, warn); err != nil {
		return err
	}
	if err := applyRestoreXattrs(path, meta, warn); err != nil {
		return err
	}
	return nil
}

func applyRestoreDirMetadata(path string, meta core.FileMeta, warn func(string, ...interface{})) error {
	if meta.Mode != 0 {
		if err := os.Chmod(path, fs.FileMode(meta.Mode)); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	if meta.Uid != 0 || meta.Gid != 0 {
		if err := os.Lchown(path, int(meta.Uid), int(meta.Gid)); err != nil && warn != nil {
			warn("could not set ownership on %s: %v", path, err)
		} else if err == nil && meta.Mode&0o6000 != 0 {
			_ = os.Chmod(path, fs.FileMode(meta.Mode))
		}
	}
	if err := applyRestoreTimes(path, meta, warn); err != nil {
		return err
	}
	if err := applyRestoreXattrs(path, meta, warn); err != nil {
		return err
	}
	return nil
}
