package cloudstic

import (
	"context"

	"github.com/cloudstic/cli/pkg/engine"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/ui"
)

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithReporter sets the progress reporter for the client.
func WithReporter(r ui.Reporter) ClientOption {
	return func(c *Client) { c.reporter = r }
}

// Client is the high-level interface for using Cloudstic as a library.
type Client struct {
	store    store.ObjectStore
	reporter ui.Reporter
}

// NewClient creates a new Cloudstic client with a configured object store.
func NewClient(s store.ObjectStore, opts ...ClientOption) *Client {
	c := &Client{
		store:    s,
		reporter: ui.NewNoOpReporter(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ---------------------------------------------------------------------------
// Backup
// ---------------------------------------------------------------------------

// BackupOption configures a Backup operation (re-exported from engine).
type BackupOption = engine.BackupOption

var (
	WithVerbose   = engine.WithVerbose
	WithTags      = engine.WithTags
	WithGenerator = engine.WithGenerator
	WithMeta      = engine.WithMeta
)

// Backup runs a backup from src and returns the result.
func (c *Client) Backup(ctx context.Context, src store.Source, opts ...BackupOption) (*engine.RunResult, error) {
	mgr := engine.NewBackupManager(src, c.store, c.reporter, opts...)
	return mgr.Run(ctx)
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

// RestoreOption configures a Restore operation (re-exported from engine).
type RestoreOption = engine.RestoreOption

// RestoreResult holds the outcome of a restore operation (re-exported from engine).
type RestoreResult = engine.RestoreResult

// Restore downloads a snapshot as files into targetPath.
func (c *Client) Restore(ctx context.Context, targetPath string, snapshotID string, opts ...RestoreOption) (*RestoreResult, error) {
	mgr := engine.NewRestoreManager(c.store, c.reporter)
	return mgr.Run(ctx, targetPath, snapshotID, opts...)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

// ListOption configures a List operation (re-exported from engine).
type ListOption = engine.ListOption

// ListResult holds the snapshots returned by a list operation (re-exported from engine).
type ListResult = engine.ListResult

// List returns all snapshots.
func (c *Client) List(ctx context.Context, opts ...ListOption) (*ListResult, error) {
	mgr := engine.NewListManager(c.store)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// LsSnapshot
// ---------------------------------------------------------------------------

// LsSnapshotOption configures a LsSnapshot operation (re-exported from engine).
type LsSnapshotOption = engine.LsSnapshotOption

// LsSnapshotResult holds the data returned by an ls-snapshot operation (re-exported from engine).
type LsSnapshotResult = engine.LsSnapshotResult

// LsSnapshot lists the contents of a snapshot.
func (c *Client) LsSnapshot(ctx context.Context, snapshotID string, opts ...LsSnapshotOption) (*LsSnapshotResult, error) {
	mgr := engine.NewLsSnapshotManager(c.store)
	return mgr.Run(ctx, snapshotID, opts...)
}

// ---------------------------------------------------------------------------
// Prune
// ---------------------------------------------------------------------------

// PruneOption configures a Prune operation (re-exported from engine).
type PruneOption = engine.PruneOption

// Prune removes unreferenced data from the store.
func (c *Client) Prune(ctx context.Context, opts ...PruneOption) (*engine.PruneResult, error) {
	mgr := engine.NewPruneManager(c.store, c.reporter)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// Forget
// ---------------------------------------------------------------------------

// ForgetOption configures a Forget operation (re-exported from engine).
type ForgetOption = engine.ForgetOption

var (
	WithPrune         = engine.WithPrune
	WithDryRun        = engine.WithDryRun
	WithKeepLast      = engine.WithKeepLast
	WithKeepHourly    = engine.WithKeepHourly
	WithKeepDaily     = engine.WithKeepDaily
	WithKeepWeekly    = engine.WithKeepWeekly
	WithKeepMonthly   = engine.WithKeepMonthly
	WithKeepYearly    = engine.WithKeepYearly
	WithGroupBy       = engine.WithGroupBy
	WithFilterTag     = engine.WithFilterTag
	WithFilterSource  = engine.WithFilterSource
	WithFilterAccount = engine.WithFilterAccount
	WithFilterPath    = engine.WithFilterPath
)

type PolicyResult = engine.PolicyResult

// Forget removes a snapshot by ID.
func (c *Client) Forget(ctx context.Context, snapshotID string, opts ...ForgetOption) (*engine.ForgetResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	return mgr.Run(ctx, snapshotID, opts...)
}

// ForgetPolicy applies a retention policy and removes snapshots not matched by any keep rule.
func (c *Client) ForgetPolicy(ctx context.Context, opts ...ForgetOption) (*PolicyResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	return mgr.RunPolicy(ctx, opts...)
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

// DiffOption configures a Diff operation (re-exported from engine).
type DiffOption = engine.DiffOption

// DiffResult holds the outcome of a diff operation (re-exported from engine).
type DiffResult = engine.DiffResult

// Diff compares two snapshots.
func (c *Client) Diff(ctx context.Context, snap1, snap2 string, opts ...DiffOption) (*DiffResult, error) {
	mgr := engine.NewDiffManager(c.store)
	return mgr.Run(ctx, snap1, snap2, opts...)
}
