//go:build !linux && !darwin

package engine

import "github.com/cloudstic/cli/internal/core"

func applyRestoreXattrs(_ string, _ core.FileMeta) error {
	return nil
}
