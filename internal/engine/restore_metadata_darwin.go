//go:build darwin

package engine

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/cloudstic/cli/internal/core"
)

func applyRestoreTimes(path string, meta core.FileMeta, warn func(string, ...interface{})) error {
	if meta.Mtime > 0 {
		mt := time.Unix(meta.Mtime, 0)
		if err := os.Chtimes(path, mt, mt); err != nil {
			return fmt.Errorf("chtimes %s: %w", path, err)
		}
	}
	if meta.Btime > 0 && warn != nil {
		warn("birth time replay is not yet implemented on macOS for %s", path)
	}
	return nil
}

func applyRestoreFlags(path string, meta core.FileMeta, warn func(string, ...interface{})) error {
	if meta.Flags == 0 {
		return nil
	}
	if err := syscall.Chflags(path, int(meta.Flags)); err != nil {
		if warn != nil {
			warn("could not set file flags on %s: %v", path, err)
			return nil
		}
		return fmt.Errorf("set file flags on %s: %w", path, err)
	}
	return nil
}
