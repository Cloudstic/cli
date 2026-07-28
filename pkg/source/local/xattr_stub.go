//go:build !linux && !darwin

package local

import "github.com/cloudstic/cli/pkg/source"

// readExtendedMeta is a no-op on platforms where extended metadata
// collection is not supported.
func readExtendedMeta(_ string, _ *source.FileMeta, _, _, _ bool, _ []string) {}

// detectFsType is a no-op on unsupported platforms.
func detectFsType(_ string) string { return "" }
