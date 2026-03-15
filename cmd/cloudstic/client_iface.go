package main

import (
	"context"
	"io"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/source"
)

// cloudsticClient is the interface commands use to interact with the repository.
type cloudsticClient interface {
	Backup(ctx context.Context, src source.Source, opts ...cloudstic.BackupOption) (*cloudstic.BackupResult, error)
	Restore(ctx context.Context, w io.Writer, snapshotRef string, opts ...cloudstic.RestoreOption) (*cloudstic.RestoreResult, error)
	RestoreToDir(ctx context.Context, outputDir, snapshotRef string, opts ...cloudstic.RestoreOption) (*cloudstic.RestoreResult, error)
	List(ctx context.Context, opts ...cloudstic.ListOption) (*cloudstic.ListResult, error)
	LsSnapshot(ctx context.Context, snapshotID string, opts ...cloudstic.LsSnapshotOption) (*cloudstic.LsSnapshotResult, error)
	Prune(ctx context.Context, opts ...cloudstic.PruneOption) (*cloudstic.PruneResult, error)
	Forget(ctx context.Context, snapshotID string, opts ...cloudstic.ForgetOption) (*cloudstic.ForgetResult, error)
	ForgetPolicy(ctx context.Context, opts ...cloudstic.ForgetOption) (*cloudstic.PolicyResult, error)
	Diff(ctx context.Context, snap1, snap2 string, opts ...cloudstic.DiffOption) (*cloudstic.DiffResult, error)
	Check(ctx context.Context, opts ...cloudstic.CheckOption) (*cloudstic.CheckResult, error)
	Cat(ctx context.Context, keys ...string) ([]*cloudstic.CatResult, error)
	BreakLock(ctx context.Context) ([]*cloudstic.RepoLock, error)
}
