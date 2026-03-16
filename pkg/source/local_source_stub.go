//go:build !darwin && !linux && !windows

package source

// detectVolumeIdentity is a stub for platforms where volume UUID detection
// is not yet implemented. It returns empty strings, causing the engine to
// fall back to legacy account+path matching.
func detectVolumeIdentity(_ string) (uuid, label, mountPoint string) {
	return "", "", ""
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
