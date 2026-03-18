//go:build linux

package engine

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/cloudstic/cli/internal/core"
	"golang.org/x/sys/unix"
)

func applyRestoreTimes(path string, meta core.FileMeta, warn func(string, ...interface{})) error {
	if meta.Mtime > 0 {
		mt := time.Unix(meta.Mtime, 0)
		if err := os.Chtimes(path, mt, mt); err != nil {
			return fmt.Errorf("chtimes %s: %w", path, err)
		}
	}
	if meta.Btime > 0 && warn != nil {
		warn("birth time replay is not supported on Linux")
	}
	return nil
}

func applyRestoreFlags(path string, meta core.FileMeta, warn func(string, ...interface{})) error {
	if meta.Flags == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s for flags: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	flags := meta.Flags
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), unix.FS_IOC_SETFLAGS, uintptr(unsafe.Pointer(&flags)))
	if errno != 0 {
		if (errors.Is(errno, unix.ENOTSUP) || errors.Is(errno, unix.EOPNOTSUPP) || errors.Is(errno, unix.EPERM) || errors.Is(errno, unix.EACCES)) && warn != nil {
			warn("could not set file flags on %s: %v", path, errno)
			return nil
		}
		return fmt.Errorf("set file flags on %s: %w", path, errno)
	}
	return nil
}
