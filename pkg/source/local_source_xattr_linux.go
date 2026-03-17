//go:build linux

package source

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// readBtime returns the file birth time via statx on Linux.
// Returns 0 if the kernel or filesystem does not support btime.
func readBtime(path string, _ *syscall.Stat_t) int64 {
	var stx unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stx)
	if err != nil {
		return 0
	}
	if stx.Mask&unix.STATX_BTIME != 0 && stx.Btime.Sec != 0 {
		return stx.Btime.Sec
	}
	return 0
}

// readFlags returns the per-file flags via FS_IOC_GETFLAGS ioctl on Linux.
// Returns 0 on filesystems that don't support it.
func readFlags(path string, _ *syscall.Stat_t) uint32 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	var flags uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), unix.FS_IOC_GETFLAGS, uintptr(unsafe.Pointer(&flags)))
	if errno != 0 {
		return 0
	}
	return flags
}

// detectFsType returns the filesystem type name for the given path on Linux.
func detectFsType(path string) string {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return ""
	}
	return fsTypeName(stat.Type)
}

// fsTypeName maps Linux filesystem magic numbers to human-readable names.
func fsTypeName(magic int64) string {
	switch magic {
	case 0xEF53:
		return "ext4"
	case 0x9123683E:
		return "btrfs"
	case 0x58465342:
		return "xfs"
	case 0x2FC12FC1:
		return "zfs"
	case 0x6969:
		return "nfs"
	case 0x01021994:
		return "tmpfs"
	case 0x5346544E:
		return "ntfs"
	case 0x4D44:
		return "fat"
	case -137439006848, 0x2011BAB0: // exfat magic (may vary)
		return "exfat"
	case 0x61756673:
		return "aufs"
	case 0x794C7630:
		return "overlayfs"
	default:
		return fmt.Sprintf("unknown:0x%X", uint64(magic))
	}
}
