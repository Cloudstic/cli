//go:build !linux && !darwin

package source

import "github.com/cloudstic/cli/internal/core"

// readExtendedMeta is a no-op on platforms where extended metadata
// collection is not supported.
func readExtendedMeta(_ string, _ *core.FileMeta, _, _, _ bool, _ []string) {}

// detectFsType is a no-op on unsupported platforms.
func detectFsType(_ string) string { return "" }
