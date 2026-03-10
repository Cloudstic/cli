//go:build !darwin && !linux

package e2e

import "testing"

// portableDriveSource is a stub for platforms where loopback/RAM disk creation
// is not implemented.
type portableDriveSource struct{}

func newPortableDriveSource(_ *testing.T) *portableDriveSource { return nil }

func (s *portableDriveSource) Name() string                                   { return "portable" }
func (s *portableDriveSource) Env() TestEnv                                   { return Hermetic }
func (s *portableDriveSource) Setup(t *testing.T) []string                    { t.Skip("portable drive not supported on this platform"); return nil }
func (s *portableDriveSource) WriteFile(t *testing.T, relPath, content string) {}
