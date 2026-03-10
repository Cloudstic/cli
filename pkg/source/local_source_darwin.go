//go:build darwin

package source

import (
	"encoding/xml"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// macOS getattrlist constants
const (
	attrBitMapCount = 5 // ATTR_BIT_MAP_COUNT
	attrVolUUID     = 0x00040000
	attrVolName     = 0x00000001
)

// attrList mirrors struct attrlist from <sys/attr.h>.
type attrList struct {
	bitmapCount uint16
	reserved    uint16
	commonAttr  uint32
	volAttr     uint32
	dirAttr     uint32
	fileAttr    uint32
	forkAttr    uint32
}

// detectVolumeIdentity returns the volume UUID, label, and mount point for
// the filesystem containing path on macOS. It prefers the GPT partition UUID
// (cross-OS stable, obtained via diskutil) over the macOS-specific volume
// UUID from getattrlist(2).
func detectVolumeIdentity(path string) (uuid, label, mountPoint string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err == nil {
		mountPoint = cStringToGo(stat.Mntonname[:])
	}

	// Try GPT partition UUID first (stable across OSes).
	if partUUID := getPartitionUUID(path); partUUID != "" {
		uuid = partUUID
	} else {
		uuid = getVolumeUUID(path)
	}
	label = getVolumeLabel(path)
	return uuid, label, mountPoint
}

// getPartitionUUID uses statfs to find the BSD device for path, then
// parses `diskutil info -plist` output for the GPT partition UUID.
func getPartitionUUID(path string) string {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return ""
	}

	// f_mntfromname is a null-terminated C string in a fixed byte array.
	device := cStringToGo(stat.Mntfromname[:])
	if device == "" || !strings.HasPrefix(device, "/dev/") {
		return ""
	}

	return parseDiskutilPartUUID(device)
}

// parseDiskutilPartUUID runs `diskutil info -plist <device>` and extracts
// the DiskUUID (GPT partition UUID) from the plist XML output.
func parseDiskutilPartUUID(device string) string {
	out, err := exec.Command("diskutil", "info", "-plist", device).Output()
	if err != nil {
		return ""
	}
	return extractPlistValue(out, "DiskUUID")
}

// extractPlistValue extracts a string value for a given key from an Apple
// plist XML. We use a simple state-machine scan to avoid depending on
// external plist libraries.
func extractPlistValue(data []byte, key string) string {
	// Apple plists use: <key>Name</key>\n<string>Value</string>
	d := xml.NewDecoder(strings.NewReader(string(data)))
	var foundKey bool
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "key" {
				var k string
				if err := d.DecodeElement(&k, &t); err == nil && k == key {
					foundKey = true
				}
			} else if foundKey && t.Name.Local == "string" {
				var v string
				if err := d.DecodeElement(&v, &t); err == nil {
					return strings.ToUpper(v)
				}
				return ""
			} else {
				foundKey = false
			}
		}
	}
}

// cStringToGo converts a null-terminated int8 or byte slice to a Go string.
func cStringToGo(b []int8) string {
	buf := make([]byte, len(b))
	for i, c := range b {
		if c == 0 {
			return string(buf[:i])
		}
		buf[i] = byte(c)
	}
	return string(buf)
}

func getVolumeUUID(path string) string {
	attrs := attrList{
		bitmapCount: attrBitMapCount,
		volAttr:     attrVolUUID,
	}

	// Response: 4-byte length + 16-byte UUID
	var buf [4 + 16]byte

	pathBytes, err := syscall.BytePtrFromString(path)
	if err != nil {
		return ""
	}

	_, _, errno := syscall.Syscall6(
		syscall.SYS_GETATTRLIST,
		uintptr(unsafe.Pointer(pathBytes)),
		uintptr(unsafe.Pointer(&attrs)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0, // options
		0,
	)
	if errno != 0 {
		return ""
	}

	u := buf[4:20]
	return fmt.Sprintf(
		"%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		u[0], u[1], u[2], u[3],
		u[4], u[5],
		u[6], u[7],
		u[8], u[9],
		u[10], u[11], u[12], u[13], u[14], u[15],
	)
}

func getVolumeLabel(path string) string {
	attrs := attrList{
		bitmapCount: attrBitMapCount,
		volAttr:     attrVolName,
	}

	// Response: 4-byte length + attrreference (4-byte offset + 4-byte length) + name data
	var buf [4 + 8 + 256]byte

	pathBytes, err := syscall.BytePtrFromString(path)
	if err != nil {
		return ""
	}

	_, _, errno := syscall.Syscall6(
		syscall.SYS_GETATTRLIST,
		uintptr(unsafe.Pointer(pathBytes)),
		uintptr(unsafe.Pointer(&attrs)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
		0,
	)
	if errno != 0 {
		return ""
	}

	// Parse attrreference: offset from start of attrreference field, then length
	if buf[0] < 12 {
		return ""
	}
	off := *(*int32)(unsafe.Pointer(&buf[4]))
	nameLen := *(*uint32)(unsafe.Pointer(&buf[8]))

	nameStart := 4 + int(off) // offset is relative to the attrreference field
	nameEnd := nameStart + int(nameLen)
	if nameStart < 0 || nameEnd > len(buf) || nameStart >= nameEnd {
		return ""
	}

	// Trim null terminator
	name := buf[nameStart:nameEnd]
	for len(name) > 0 && name[len(name)-1] == 0 {
		name = name[:len(name)-1]
	}
	return string(name)
}

// VolumeUUID returns the detected or overridden volume UUID.
func (s *LocalSource) VolumeUUID() string {
	return s.volumeUUID
}

// VolumeLabel returns the detected volume label.
func (s *LocalSource) VolumeLabel() string {
	return s.volumeLabel
}
