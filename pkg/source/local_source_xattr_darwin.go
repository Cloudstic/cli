//go:build darwin

package source

import (
	"syscall"
)

// readBtime returns the file birth time from macOS stat.
func readBtime(_ string, st *syscall.Stat_t) int64 {
	return st.Birthtimespec.Sec
}

// readFlags returns the per-file flags from macOS stat (UF_IMMUTABLE, etc.).
func readFlags(_ string, st *syscall.Stat_t) uint32 {
	return st.Flags
}

// detectFsType returns the filesystem type name for the given path on macOS.
func detectFsType(path string) string {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return ""
	}
	// F_fstypename is a null-terminated char array on macOS.
	name := make([]byte, 0, len(stat.Fstypename))
	for _, c := range stat.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	return string(name)
}
