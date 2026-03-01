package cloudstic

import (
	"context"
	"fmt"
	"io"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

var log = logger.New("client", logger.ColorCyan)

// ---------------------------------------------------------------------------
// Re-exported types from internal packages
// ---------------------------------------------------------------------------

// RepoConfig is the repository marker written by "init".
type RepoConfig = core.RepoConfig

// Reporter defines the interface for progress reporting.
type Reporter = ui.Reporter

// Phase represents an active progress tracking phase.
type Phase = ui.Phase

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithReporter sets the progress reporter for the client.
func WithReporter(r Reporter) ClientOption {
	return func(c *Client) { c.reporter = r }
}

// WithEncryptionKey enables AES-256-GCM encryption. Key must be 32 bytes.
// The HMAC deduplication key is automatically derived from this key.
func WithEncryptionKey(key []byte) ClientOption {
	return func(c *Client) { c.encryptionKey = key }
}

// WithPackfile enables bundling small objects into 8MB packs to save API calls.
func WithPackfile(enable bool) ClientOption {
	return func(c *Client) { c.enablePackfile = enable }
}

// Client is the high-level interface for using Cloudstic as a library.
// Callers should defer Client.Close() to release temporary resources.
type Client struct {
	store          store.ObjectStore
	storedMeter    *store.MeteredStore
	packStore      *store.PackStore
	encryptionKey  []byte
	hmacKey        []byte
	enablePackfile bool
	reporter       ui.Reporter
}

func NewClient(base store.ObjectStore, opts ...ClientOption) (*Client, error) {
	c := &Client{
		reporter: ui.NewNoOpReporter(),
	}
	for _, opt := range opts {
		opt(c)
	}

	// Derive HMAC dedup key from the encryption key.
	// This avoids plumbing two keys through the entire stack while
	// keeping the HMAC key cryptographically independent (HKDF is a PRF).
	if len(c.encryptionKey) > 0 {
		hmacKey, err := crypto.DeriveKey(c.encryptionKey, crypto.HKDFInfoDedupV1)
		if err != nil {
			return nil, fmt.Errorf("derive HMAC dedup key: %w", err)
		}
		c.hmacKey = hmacKey
	}

	inner := base

	log.Debugf("packfile enabled: %v", c.enablePackfile)
	if c.enablePackfile {
		ps, err := store.NewPackStore(inner)
		if err != nil {
			return nil, fmt.Errorf("init packstore: %w", err)
		}
		c.packStore = ps
		inner = ps
	}

	storedMeter := store.NewMeteredStore(inner)
	inner = storedMeter
	if len(c.encryptionKey) > 0 {
		inner = store.NewEncryptedStore(storedMeter, c.encryptionKey)
	}

	c.store = store.NewCompressedStore(inner)
	c.storedMeter = storedMeter
	return c, nil
}

func (c *Client) Store() store.ObjectStore { return c.store }

// Close releases temporary resources (e.g. bbolt catalog files).
func (c *Client) Close() error {
	if c.packStore != nil {
		return c.packStore.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Backup
// ---------------------------------------------------------------------------

type BackupOption = engine.BackupOption
type BackupResult = engine.RunResult

var (
	WithVerbose      = engine.WithVerbose
	WithBackupDryRun = engine.WithBackupDryRun
	WithTags         = engine.WithTags
	WithGenerator    = engine.WithGenerator
	WithMeta         = engine.WithMeta
)

func (c *Client) Backup(ctx context.Context, src store.Source, opts ...BackupOption) (*BackupResult, error) {
	rawMeter := store.NewMeteredStore(c.store)
	c.storedMeter.Reset()

	mgr, err := engine.NewBackupManager(src, rawMeter, c.reporter, c.hmacKey, opts...)
	if err != nil {
		return nil, err
	}
	defer mgr.Close()
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

var (
	WithRestoreDryRun  = engine.WithRestoreDryRun
	WithRestoreVerbose = engine.WithRestoreVerbose
)

// Restore writes the snapshot's file tree as a ZIP archive to w.
// snapshotRef can be "", "latest", a bare hash, or "snapshot/<hash>".
func (c *Client) Restore(ctx context.Context, w io.Writer, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	mgr := engine.NewRestoreManager(c.store, c.reporter)
	return mgr.Run(ctx, w, snapshotRef, opts...)
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
type PruneResult = engine.PruneResult

var (
	WithPruneDryRun  = engine.WithPruneDryRun
	WithPruneVerbose = engine.WithPruneVerbose
)

func (c *Client) Prune(ctx context.Context, opts ...PruneOption) (*PruneResult, error) {
	mgr := engine.NewPruneManager(c.store, c.reporter)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// Forget
// ---------------------------------------------------------------------------

type ForgetOption = engine.ForgetOption
type ForgetResult = engine.ForgetResult

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

func (c *Client) Forget(ctx context.Context, snapshotID string, opts ...ForgetOption) (*ForgetResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	return mgr.Run(ctx, snapshotID, opts...)
}

func (c *Client) ForgetPolicy(ctx context.Context, opts ...ForgetOption) (*PolicyResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	return mgr.RunPolicy(ctx, opts...)
}

// ---------------------------------------------------------------------------
// BreakLock
// ---------------------------------------------------------------------------

type RepoLock = engine.RepoLock

func (c *Client) BreakLock(ctx context.Context) ([]*RepoLock, error) {
	return engine.BreakRepoLock(ctx, c.store)
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
