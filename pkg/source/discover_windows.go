//go:build windows

package source

import (
	"strings"

	"golang.org/x/sys/windows"
)

func discoverLocalCandidates() ([]discoverCandidate, error) {
	buf := make([]uint16, 256)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0])
	if err != nil {
		return nil, err
	}

	var candidates []discoverCandidate
	for _, mountPoint := range strings.Split(windows.UTF16ToString(buf[:n]), "\x00") {
		if mountPoint == "" {
			continue
		}
		typ := windows.GetDriveType(windows.StringToUTF16Ptr(mountPoint))
		switch typ {
		case windows.DRIVE_FIXED:
			candidates = append(candidates, discoverCandidate{
				mountPoint: mountPoint,
				portable:   !strings.EqualFold(mountPoint, `C:\`),
			})
		case windows.DRIVE_REMOVABLE:
			candidates = append(candidates, discoverCandidate{mountPoint: mountPoint, portable: true})
		}
	}
	return candidates, nil
}
