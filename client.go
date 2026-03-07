package cloudstic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/source"
	"github.com/cloudstic/cli/pkg/store"
)

var log = logger.New("client", logger.ColorCyan)

// ---------------------------------------------------------------------------
// Init (operates on the raw store, before encryption is set up)
// ---------------------------------------------------------------------------

type InitOption = engine.InitOption
type InitResult = engine.InitResult

var (
	WithInitCredentials  = engine.WithInitCredentials
	WithInitRecovery     = engine.WithInitRecovery
	WithInitNoEncryption = engine.WithInitNoEncryption
	WithInitAdoptSlots   = engine.WithInitAdoptSlots
)

// InitRepo bootstraps a new repository on the given raw (undecorated) store.
// This is a package-level function because init runs before the full
// Client decorator chain (encryption, compression, packfiles) is set up.
func InitRepo(ctx context.Context, rawStore store.ObjectStore, opts ...InitOption) (*InitResult, error) {
	mgr := engine.NewInitManager(rawStore)
	return mgr.Run(ctx, opts...)
}

// requireEncryptedRepo loads the repository config and returns an error if
// the repository has not been initialized or does not use encryption.
func requireEncryptedRepo(ctx context.Context, rawStore store.ObjectStore) error {
	cfg, err := LoadRepoConfig(ctx, rawStore)
	if err != nil {
		return fmt.Errorf("read repository config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("repository not initialized -- run 'cloudstic init' first")
	}
	if !cfg.Encrypted {
		return fmt.Errorf("repository is not encrypted")
	}
	return nil
}

// ListKeySlots returns all encryption key slots in the repository.
// Returns an error if the repository is not initialized or not encrypted.
func ListKeySlots(ctx context.Context, rawStore store.ObjectStore) ([]KeySlot, error) {
	if err := requireEncryptedRepo(ctx, rawStore); err != nil {
		return nil, err
	}
	slots, err := keychain.LoadKeySlots(rawStore)
	if err != nil {
		return nil, fmt.Errorf("load key slots: %w", err)
	}
	return slots, nil
}

// ChangePassword replaces the password key slot using the provided keychain
// to authenticate and newPassword as the new passphrase.
func ChangePassword(ctx context.Context, rawStore store.ObjectStore, kc keychain.Chain, pwd PasswordProvider) error {
	if err := requireEncryptedRepo(ctx, rawStore); err != nil {
		return err
	}
	slots, err := keychain.LoadKeySlots(rawStore)
	if err != nil {
		return fmt.Errorf("load key slots: %w", err)
	}
	masterKey, err := kc.Resolve(ctx, slots)
	if err != nil {
		return fmt.Errorf("unlock repository: %w", err)
	}
	newPassword, err := pwd.NewPassword(ctx)
	if err != nil {
		return err
	}
	return keychain.ChangePasswordSlot(rawStore, masterKey, newPassword)
}

// AddRecoveryKey generates a BIP39 recovery key for the repository,
// authenticating with kc to obtain the master key.
// Returns the 24-word mnemonic phrase.
func AddRecoveryKey(ctx context.Context, rawStore store.ObjectStore, kc keychain.Chain) (string, error) {
	if err := requireEncryptedRepo(ctx, rawStore); err != nil {
		return "", err
	}
	slots, err := keychain.LoadKeySlots(rawStore)
	if err != nil {
		return "", fmt.Errorf("load key slots: %w", err)
	}
	masterKey, err := kc.Resolve(ctx, slots)
	if err != nil {
		return "", fmt.Errorf("unlock repository: %w", err)
	}
	return keychain.AddRecoverySlot(rawStore, masterKey)
}

// LoadRepoConfig reads the repository marker from a raw (undecorated) store.
// Returns (nil, nil) if the repository has not been initialized yet.
// Returns an error if the store is unreachable (e.g. invalid credentials).
func LoadRepoConfig(ctx context.Context, rawStore store.ObjectStore) (*RepoConfig, error) {
	exists, err := rawStore.Exists(ctx, "config")
	if err != nil {
		return nil, fmt.Errorf("check repo config: %w", err)
	}
	if !exists {
		return nil, nil // repository not initialized
	}

	data, err := rawStore.Get(ctx, "config")
	if err != nil {
		return nil, fmt.Errorf("read repo config: %w", err)
	}
	if data == nil {
		return nil, nil
	}
	var cfg core.RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse repo config: %w", err)
	}
	return &cfg, nil
}

// ---------------------------------------------------------------------------
// Re-exported types from internal packages
// ---------------------------------------------------------------------------

// RepoConfig is the repository marker written by "init".
type RepoConfig = core.RepoConfig

// Reporter defines the interface for progress reporting.
type Reporter = ui.Reporter

// Phase represents an active progress tracking phase.
type Phase = ui.Phase

// KeySlot is re-exported for callers that need to inspect slot metadata.
type KeySlot = keychain.KeySlot

// KMSClient is re-exported for callers that provide KMS credentials.
type KMSClient = crypto.KMSClient

// PasswordProvider supplies a new password when prompted. It is used by
// ChangePassword to obtain the replacement passphrase. Implementations may
// prompt the user interactively, derive a password programmatically, or
// return a static value.
type PasswordProvider interface {
	NewPassword(ctx context.Context) (string, error)
}

// PasswordProviderFunc is a function adapter for PasswordProvider.
// Any func(context.Context) (string, error) can be used as a PasswordProvider:
//
//	client.ChangePassword(ctx, store, creds, cloudstic.PasswordProviderFunc(func(ctx context.Context) (string, error) {
//		return promptUser("New password: ")
//	}))
type PasswordProviderFunc func(ctx context.Context) (string, error)

func (f PasswordProviderFunc) NewPassword(ctx context.Context) (string, error) { return f(ctx) }

// PasswordString is a PasswordProvider that returns a fixed string.
// Use this when the new password is already known at call time:
//
//	client.ChangePassword(ctx, store, creds, cloudstic.PasswordString("my-new-password"))
type PasswordString string

func (p PasswordString) NewPassword(ctx context.Context) (string, error) { return string(p), nil }

// ---------------------------------------------------------------------------
// client
// ---------------------------------------------------------------------------

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithReporter sets the progress reporter for the client.
func WithReporter(r Reporter) ClientOption {
	return func(c *Client) { c.reporter = r }
}

// WithEncryptionKey directly sets the AES-256-GCM encryption key (32 bytes).
// This bypasses repo config detection and unconditionally applies encryption.
// The HMAC deduplication key is automatically derived from this key.
// Use this for the SaaS product where the key is already resolved externally.
func WithEncryptionKey(key []byte) ClientOption {
	return func(c *Client) { c.encryptionKey = key }
}

// WithKeychain sets a Keychain for automatic master key resolution. During
// NewClient, the repo config is read from the store; if the repository is
// encrypted, Resolve is called to obtain the master key and the
// encryption key is derived. If the repository is not encrypted, the keychain
// is silently ignored.
func WithKeychain(kc keychain.Chain) ClientOption {
	return func(c *Client) { c.keychain = kc }
}

// WithPackfile enables bundling small objects into 8MB packs to save API calls.
func WithPackfile(enable bool) ClientOption {
	return func(c *Client) { c.enablePackfile = enable }
}

// Client is the high-level interface for using Cloudstic as a library.
type Client struct {
	store          store.ObjectStore
	storedMeter    *store.MeteredStore
	encryptionKey  []byte
	hmacKey        []byte
	keychain       keychain.Chain
	enablePackfile bool
	reporter       ui.Reporter
}

func NewClient(base store.ObjectStore, opts ...ClientOption) (*Client, error) {
	c := &Client{
		enablePackfile: true, // Packfile is enabled by default
		reporter:       ui.NewNoOpReporter(),
	}
	for _, opt := range opts {
		opt(c)
	}

	// Auto-detect encryption from the repo config if no explicit key is set.
	if len(c.encryptionKey) == 0 {
		encKey, err := c.resolveKeyFromConfig(base)
		if err != nil {
			return nil, err
		}
		c.encryptionKey = encKey
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
		packStore, err := store.NewPackStore(inner)
		if err != nil {
			return nil, fmt.Errorf("init packstore: %w", err)
		}
		inner = packStore
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

// resolveKeyFromConfig reads the repo config and, if the repository is
// encrypted, uses the Keychain to resolve the master key and derive the encryption key.
func (c *Client) resolveKeyFromConfig(base store.ObjectStore) ([]byte, error) {
	ctx := context.Background()
	cfg, err := LoadRepoConfig(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("read repo config: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("repository not initialized -- run 'cloudstic init' first")
	}
	if !cfg.Encrypted {
		return nil, nil
	}
	slots, err := keychain.LoadKeySlots(base)
	if err != nil {
		return nil, fmt.Errorf("load key slots: %w", err)
	}
	masterKey, err := c.keychain.Resolve(ctx, slots)
	if err != nil {
		return nil, err
	}
	return keychain.DeriveEncryptionKey(masterKey)
}

func (c *Client) Store() store.ObjectStore { return c.store }

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
	WithExcludeHash  = engine.WithExcludeHash
)

func (c *Client) Backup(ctx context.Context, src source.Source, opts ...BackupOption) (*BackupResult, error) {
	rawMeter := store.NewMeteredStore(c.store)
	c.storedMeter.Reset()

	mgr := engine.NewBackupManager(src, rawMeter, c.reporter, c.hmacKey, opts...)
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
	WithRestorePath    = engine.WithRestorePath
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

var WithListVerbose = engine.WithListVerbose

func (c *Client) List(ctx context.Context, opts ...ListOption) (*ListResult, error) {
	mgr := engine.NewListManager(c.store)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// LsSnapshot
// ---------------------------------------------------------------------------

type LsSnapshotOption = engine.LsSnapshotOption
type LsSnapshotResult = engine.LsSnapshotResult

var WithLsVerbose = engine.WithLsVerbose

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
	WithForgetVerbose = engine.WithForgetVerbose
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

var WithDiffVerbose = engine.WithDiffVerbose

func (c *Client) Diff(ctx context.Context, snap1, snap2 string, opts ...DiffOption) (*DiffResult, error) {
	mgr := engine.NewDiffManager(c.store)
	return mgr.Run(ctx, snap1, snap2, opts...)
}

// ---------------------------------------------------------------------------
// Check
// ---------------------------------------------------------------------------

type CheckOption = engine.CheckOption
type CheckResult = engine.CheckResult
type CheckError = engine.CheckError

var (
	WithReadData     = engine.WithReadData
	WithCheckVerbose = engine.WithCheckVerbose
	WithSnapshotRef  = engine.WithSnapshotRef
)

// Check verifies the integrity of the repository by walking the full
// reference chain (snapshots → HAMT nodes → filemeta → content → chunks)
// and checking that every referenced object can be read.
// With WithReadData(), chunk data is re-hashed for byte-level verification.
func (c *Client) Check(ctx context.Context, opts ...CheckOption) (*CheckResult, error) {
	mgr := engine.NewCheckManager(c.store, c.reporter, c.hmacKey)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// Cat
// ---------------------------------------------------------------------------

// CatResult contains the raw data for an object key.
type CatResult struct {
	Key  string // The object key requested
	Data []byte // Raw object data (typically JSON)
}

// Cat fetches the raw data for one or more object keys from the repository.
// Object keys can be snapshot/<hash>, filemeta/<hash>, content/<hash>,
// node/<hash>, chunk/<hash>, config, index/latest, keys/<slot>, etc.
//
// This is useful for debugging, inspection, and understanding the internal
// structure of the repository.
func (c *Client) Cat(ctx context.Context, keys ...string) ([]*CatResult, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("at least one object key is required")
	}

	results := make([]*CatResult, 0, len(keys))
	for _, key := range keys {
		data, err := c.store.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("fetch object %q: %w", key, err)
		}
		if data == nil {
			return nil, fmt.Errorf("object not found: %q", key)
		}
		results = append(results, &CatResult{
			Key:  key,
			Data: data,
		})
	}
	return results, nil
}
