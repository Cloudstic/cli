//go:build windows

package source

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// ioctlDiskGetPartitionInfoEx is IOCTL_DISK_GET_PARTITION_INFO_EX.
	// Returns PARTITION_INFORMATION_EX containing the GPT partition UUID.
	ioctlDiskGetPartitionInfoEx = 0x00070048

	partitionStyleGPT = 1
)

// partitionInfoGPT mirrors the GPT variant of the PARTITION_INFORMATION union.
type partitionInfoGPT struct {
	PartitionType windows.GUID
	PartitionId   windows.GUID
	Attributes    uint64
	Name          [36]uint16
}

// partitionInformationEx mirrors the Windows PARTITION_INFORMATION_EX struct.
// The union at the end is defined as the GPT variant (the larger of the two).
type partitionInformationEx struct {
	PartitionStyle   uint32
	_                uint32 // padding for int64 alignment
	StartingOffset   int64
	PartitionLength  int64
	PartitionNumber  uint32
	RewritePartition byte
	_                [3]byte // padding to align union
	GPT              partitionInfoGPT
}

func detectVolumeIdentity(path string) (uuid, label, mountPoint string) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", "", ""
	}

	// Get the volume mount point (e.g. "C:\").
	var volPath [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumePathName(pathUTF16, &volPath[0], uint32(len(volPath))); err != nil {
		return "", "", ""
	}
	mountPoint = windows.UTF16ToString(volPath[:])

	label = getVolumeLabel(mountPoint)
	uuid = getPartitionUUID(mountPoint)

	return uuid, label, mountPoint
}

// getVolumeLabel returns the volume label via GetVolumeInformation.
func getVolumeLabel(mountPoint string) string {
	mountUTF16, err := windows.UTF16PtrFromString(mountPoint)
	if err != nil {
		return ""
	}
	var volumeName [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumeInformation(
		mountUTF16,
		&volumeName[0], uint32(len(volumeName)),
		nil, nil, nil, nil, 0,
	); err != nil {
		return ""
	}
	return windows.UTF16ToString(volumeName[:])
}

// getPartitionUUID returns the GPT partition UUID via DeviceIoControl.
// Returns empty for MBR partitions or on error.
func getPartitionUUID(mountPoint string) string {
	// Open the volume: "C:\" → "\\.\C:" (strip trailing backslash).
	volumePath := `\\.\` + strings.TrimRight(mountPoint, `\`)
	pathUTF16, err := windows.UTF16PtrFromString(volumePath)
	if err != nil {
		return ""
	}

	handle, err := windows.CreateFile(
		pathUTF16,
		0, // no access required for partition info queries
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)

	var info partitionInformationEx
	var bytesReturned uint32
	if err := windows.DeviceIoControl(
		handle,
		ioctlDiskGetPartitionInfoEx,
		nil, 0,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		&bytesReturned,
		nil,
	); err != nil {
		return ""
	}

	if info.PartitionStyle != partitionStyleGPT {
		return ""
	}

	return strings.ToUpper(formatGUID(info.GPT.PartitionId))
}

// formatGUID formats a Windows GUID as a standard UUID string.
func formatGUID(g windows.GUID) string {
	return fmt.Sprintf("%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		g.Data1, g.Data2, g.Data3,
		g.Data4[0], g.Data4[1],
		g.Data4[2], g.Data4[3], g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7],
	)
}

// VolumeUUID returns the detected or overridden volume UUID.
func (s *LocalSource) VolumeUUID() string {
	return s.volumeUUID
}

// VolumeLabel returns the detected volume label.
func (s *LocalSource) VolumeLabel() string {
	return s.volumeLabel
}
