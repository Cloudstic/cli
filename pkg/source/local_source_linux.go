//go:build linux

package source

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// detectVolumeIdentity returns the volume UUID, label, and mount point for
// the filesystem containing path on Linux. It prefers the GPT partition UUID
// (cross-OS stable) over the filesystem UUID (platform-specific).
func detectVolumeIdentity(path string) (uuid, label, mountPoint string) {
	device, mnt, err := deviceForPath(path)
	if err != nil || device == "" {
		return "", "", ""
	}
	mountPoint = mnt

	// Prefer GPT partition UUID (stable across OSes) over filesystem UUID.
	uuid = findPartUUIDForDevice(device)
	if uuid == "" {
		uuid = findUUIDForDevice(device)
	}
	label = findLabelForDevice(device)
	return uuid, label, mountPoint
}

// deviceForPath finds the mount device and mount point for a given filesystem
// path by parsing /proc/mounts and matching on device ID (stat.Dev).
func deviceForPath(path string) (device, mountPoint string, err error) {
	path = filepath.Clean(path)

	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", "", err
	}
	targetDev := st.Dev

	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = f.Close() }()

	var bestDevice string
	var bestMount string
	var bestMountLen int

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		mnt := unescapeMountField(fields[1])

		// Check if this mount point is a path prefix with a segment boundary.
		if !hasPathPrefix(path, mnt) {
			continue
		}
		// Use the longest matching mount point (most specific)
		if len(mnt) > bestMountLen {
			var mountStat syscall.Stat_t
			if err := syscall.Stat(mnt, &mountStat); err == nil && mountStat.Dev == targetDev {
				bestDevice = fields[0]
				bestMount = mnt
				bestMountLen = len(mnt)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}

	return bestDevice, bestMount, nil
}

func hasPathPrefix(path, prefix string) bool {
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}

func unescapeMountField(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}

	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+3 >= len(s) {
			out = append(out, s[i])
			continue
		}
		if !isOctalDigit(s[i+1]) || !isOctalDigit(s[i+2]) || !isOctalDigit(s[i+3]) {
			out = append(out, s[i])
			continue
		}
		v, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			out = append(out, s[i])
			continue
		}
		out = append(out, byte(v))
		i += 3
	}
	return string(out)
}

func isOctalDigit(b byte) bool {
	return b >= '0' && b <= '7'
}

// findUUIDForDevice scans /dev/disk/by-uuid/ for a symlink pointing to the
// given device and returns the UUID (the symlink name).
func findUUIDForDevice(device string) string {
	entries, err := os.ReadDir("/dev/disk/by-uuid")
	if err != nil {
		return ""
	}

	deviceBase := filepath.Base(device)
	for _, e := range entries {
		target, err := os.Readlink("/dev/disk/by-uuid/" + e.Name())
		if err != nil {
			continue
		}
		if filepath.Base(target) == deviceBase {
			return strings.ToUpper(e.Name())
		}
	}
	return ""
}

// findPartUUIDForDevice scans /dev/disk/by-partuuid/ for a symlink pointing
// to the given device and returns the GPT partition UUID. This UUID is stable
// across operating systems for the same physical partition.
func findPartUUIDForDevice(device string) string {
	entries, err := os.ReadDir("/dev/disk/by-partuuid")
	if err != nil {
		return ""
	}

	deviceBase := filepath.Base(device)
	for _, e := range entries {
		target, err := os.Readlink("/dev/disk/by-partuuid/" + e.Name())
		if err != nil {
			continue
		}
		if filepath.Base(target) == deviceBase {
			return strings.ToUpper(e.Name())
		}
	}
	return ""
}

// findLabelForDevice scans /dev/disk/by-label/ for a symlink pointing to the
// given device and returns the label (the symlink name).
func findLabelForDevice(device string) string {
	entries, err := os.ReadDir("/dev/disk/by-label")
	if err != nil {
		return ""
	}

	deviceBase := filepath.Base(device)
	for _, e := range entries {
		target, err := os.Readlink("/dev/disk/by-label/" + e.Name())
		if err != nil {
			continue
		}
		if filepath.Base(target) == deviceBase {
			return e.Name()
		}
	}
	return ""
}

// VolumeUUID returns the detected or overridden volume UUID.
// Exported for testing.
func (s *LocalSource) VolumeUUID() string {
	return s.volumeUUID
}

// VolumeLabel returns the detected volume label.
// Exported for testing.
func (s *LocalSource) VolumeLabel() string {
	return s.volumeLabel
}
