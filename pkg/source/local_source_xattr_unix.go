//go:build linux || darwin

package source

import (
	"strings"
	"syscall"

	"github.com/cloudstic/cli/internal/core"
	"golang.org/x/sys/unix"
)

// readExtendedMeta populates Mode, Uid, Gid, Btime, Flags, and Xattrs on
// the given FileMeta by inspecting the file at path. The skip flags control
// which metadata groups are collected.
func readExtendedMeta(path string, meta *core.FileMeta, skipMode, skipFlags, skipXattrs bool, xattrNamespaces []string) {
	if !skipMode {
		var st syscall.Stat_t
		if err := syscall.Lstat(path, &st); err == nil {
			meta.Mode = uint32(st.Mode) & 0xFFF
			meta.Uid = st.Uid
			meta.Gid = st.Gid
			meta.Btime = readBtime(path, &st)
			if !skipFlags {
				meta.Flags = readFlags(path, &st)
			}
		}
	}

	if !skipXattrs {
		meta.Xattrs = listXattrs(path, xattrNamespaces)
	}
}

// listXattrs retrieves all extended attributes for path, optionally filtered
// by namespace prefixes. Returns nil if there are no attributes or on error.
func listXattrs(path string, namespaces []string) map[string][]byte {
	sz, err := unix.Listxattr(path, nil)
	if err != nil || sz <= 0 {
		return nil
	}

	buf := make([]byte, sz)
	sz, err = unix.Listxattr(path, buf)
	if err != nil || sz <= 0 {
		return nil
	}

	// Parse null-separated attribute names.
	names := splitXattrNames(buf[:sz])
	if len(names) == 0 {
		return nil
	}

	xattrs := make(map[string][]byte, len(names))
	for _, name := range names {
		if len(namespaces) > 0 && !hasPrefix(name, namespaces) {
			continue
		}
		val, err := getXattr(path, name)
		if err != nil {
			continue // attribute may have been removed between list and get
		}
		xattrs[name] = val
	}

	if len(xattrs) == 0 {
		return nil
	}
	return xattrs
}

// getXattr retrieves a single extended attribute value.
func getXattr(path, name string) ([]byte, error) {
	sz, err := unix.Getxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	if sz == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, sz)
	sz, err = unix.Getxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:sz], nil
}

// splitXattrNames splits a null-separated list of attribute names.
func splitXattrNames(buf []byte) []string {
	var names []string
	for len(buf) > 0 {
		idx := 0
		for idx < len(buf) && buf[idx] != 0 {
			idx++
		}
		if idx > 0 {
			names = append(names, string(buf[:idx]))
		}
		buf = buf[idx:]
		if len(buf) > 0 {
			buf = buf[1:] // skip null terminator
		}
	}
	return names
}

func hasPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
