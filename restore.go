package cloudstic

import (
	"context"
	"io"

	"github.com/cloudstic/cli/internal/engine"
)

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

type RestoreOption = engine.RestoreOption
type RestoreResult = engine.RestoreResult

var (
	// ErrSnapshotNotFound means no snapshot matched a requested reference.
	ErrSnapshotNotFound = engine.ErrSnapshotNotFound
	// ErrSnapshotRefAmbiguous means more than one snapshot matched a hash prefix.
	ErrSnapshotRefAmbiguous = engine.ErrSnapshotRefAmbiguous

	WithRestoreDryRun   = engine.WithRestoreDryRun
	WithRestorePath     = engine.WithRestorePath
	WithRestoreNoVerify = engine.WithRestoreNoVerify
)

// Restore writes the snapshot's file tree as a ZIP archive to w.
// snapshotRef can be "", "latest", a bare hash or unambiguous hash prefix, or
// "snapshot/<hash-or-prefix>". An ambiguous prefix is rejected.
func (c *Client) Restore(ctx context.Context, w io.Writer, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	mgr := engine.NewRestoreManager(c.engineDeps())
	return mgr.Run(ctx, engine.NewZipRestoreWriter(w), snapshotRef, opts...)
}

// RestoreToDir writes the snapshot's file tree directly into outputDir.
// snapshotRef can be "", "latest", a bare hash or unambiguous hash prefix, or
// "snapshot/<hash-or-prefix>". An ambiguous prefix is rejected.
func (c *Client) RestoreToDir(ctx context.Context, outputDir, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	mgr := engine.NewRestoreManager(c.engineDeps())
	writer, err := engine.NewFSRestoreWriter(outputDir)
	if err != nil {
		return nil, err
	}
	return mgr.Run(ctx, writer, snapshotRef, opts...)
}
