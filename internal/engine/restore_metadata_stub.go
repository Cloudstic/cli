//go:build !linux && !darwin

package engine

import "github.com/cloudstic/cli/internal/core"

func applyRestoreFileMetadata(_ string, _ core.FileMeta, _ func(string, ...interface{})) error {
	return nil
}

func applyRestoreDirMetadata(_ string, _ core.FileMeta, _ func(string, ...interface{})) error {
	return nil
}

func applyRestoreFlags(_ string, _ core.FileMeta, _ func(string, ...interface{})) error {
	return nil
}
