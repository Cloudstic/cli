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

// WithEncryptionKey enables AES-256-GCM encryption. Key must be 32 bytes.
func WithEncryptionKey(key []byte) ClientOption {
	return func(c *Client) { c.encryptionKey = key }
}

// Client is the high-level interface for using Cloudstic as a library.
type Client struct {
	store         store.ObjectStore
	storedMeter   *store.MeteredStore
	encryptionKey []byte
	reporter      ui.Reporter
}

func NewClient(base store.ObjectStore, opts ...ClientOption) *Client {
	c := &Client{
		reporter: ui.NewNoOpReporter(),
	}
	for _, opt := range opts {
		opt(c)
	}

	storedMeter := store.NewMeteredStore(base)
	var inner store.ObjectStore = storedMeter
	if len(c.encryptionKey) > 0 {
		inner = store.NewEncryptedStore(storedMeter, c.encryptionKey)
	}

	c.store = store.NewCompressedStore(inner)
	c.storedMeter = storedMeter
	return c
}

func (c *Client) Store() store.ObjectStore { return c.store }

// ---------------------------------------------------------------------------
// Backup
// ---------------------------------------------------------------------------

type BackupOption = engine.BackupOption

var (
	WithVerbose   = engine.WithVerbose
	WithTags      = engine.WithTags
	WithGenerator = engine.WithGenerator
	WithMeta      = engine.WithMeta
)

func (c *Client) Backup(ctx context.Context, src store.Source, opts ...BackupOption) (*engine.RunResult, error) {
	rawMeter := store.NewMeteredStore(c.store)
	c.storedMeter.Reset()

	mgr := engine.NewBackupManager(src, rawMeter, c.reporter, opts...)
	result, err := mgr.Run(ctx)
	if err != nil {
		return nil, err
	}

	result.BytesAddedRaw = rawMeter.BytesWritten()
	result.BytesAddedStored = c.storedMeter.BytesWritten()
	return result, nil
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

type RestoreOption = engine.RestoreOption
type RestoreResult = engine.RestoreResult

func (c *Client) Restore(ctx context.Context, targetPath string, snapshotID string, opts ...RestoreOption) (*RestoreResult, error) {
	mgr := engine.NewRestoreManager(c.store, c.reporter)
	return mgr.Run(ctx, targetPath, snapshotID, opts...)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

type ListOption = engine.ListOption
type ListResult = engine.ListResult

func (c *Client) List(ctx context.Context, opts ...ListOption) (*ListResult, error) {
	mgr := engine.NewListManager(c.store)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// LsSnapshot
// ---------------------------------------------------------------------------

type LsSnapshotOption = engine.LsSnapshotOption
type LsSnapshotResult = engine.LsSnapshotResult

func (c *Client) LsSnapshot(ctx context.Context, snapshotID string, opts ...LsSnapshotOption) (*LsSnapshotResult, error) {
	mgr := engine.NewLsSnapshotManager(c.store)
	return mgr.Run(ctx, snapshotID, opts...)
}

// ---------------------------------------------------------------------------
// Prune
// ---------------------------------------------------------------------------

type PruneOption = engine.PruneOption

func (c *Client) Prune(ctx context.Context, opts ...PruneOption) (*engine.PruneResult, error) {
	mgr := engine.NewPruneManager(c.store, c.reporter)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// Forget
// ---------------------------------------------------------------------------

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

func (c *Client) Forget(ctx context.Context, snapshotID string, opts ...ForgetOption) (*engine.ForgetResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	return mgr.Run(ctx, snapshotID, opts...)
}

func (c *Client) ForgetPolicy(ctx context.Context, opts ...ForgetOption) (*PolicyResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	return mgr.RunPolicy(ctx, opts...)
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

type DiffOption = engine.DiffOption
type DiffResult = engine.DiffResult

func (c *Client) Diff(ctx context.Context, snap1, snap2 string, opts ...DiffOption) (*DiffResult, error) {
	mgr := engine.NewDiffManager(c.store)
	return mgr.Run(ctx, snap1, snap2, opts...)
}
