//go:build !linux && !darwin

package engine

import "github.com/cloudstic/cli/internal/core"

func applyRestoreXattrs(_ string, _ core.FileMeta, _ func(string, ...interface{})) error {
	return nil
}
