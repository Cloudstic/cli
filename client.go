package cloudstic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"

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

// UpgradeRepoFormat raises a repository's recorded format version to `to`,
// leaving it alone if it already meets or exceeds that.
//
// Repositories are upgraded in place and partially: new structures are written
// in the current format while older ones are read as they are and rewritten
// only opportunistically. A repository is therefore a permanent mixture of
// eras, and its recorded version is not a claim that migration finished. It is
// the *minimum reader version*: the oldest build that can still read everything
// the repository now contains.
//
// Call this from the write path that first stores something an older build
// would misread — at the moment of that write, not on mere access. Stamping a
// repository just because a newer binary opened it would lock older builds out
// of data they can still read correctly, which is the same harm the version
// gate exists to prevent.
//
// A mutation calls it — never a read — so a repository written by this build
// tells other machines sharing it to upgrade. When it stamps relative to the
// write depends on the mutation: prune and forget stamp afterwards (best-effort,
// via Client.stampWriteFormat), because what they write is decoded correctly at
// either format; backup stamps beforehand (fatal, via Client.raiseRepoFormat),
// because it writes content whose encoding an older build would misread and
// which cannot be rewritten once stored. See core.FramedCompressionFormat and
// docs/compatibility.md.
func UpgradeRepoFormat(ctx context.Context, rawStore store.ObjectStore, to int) error {
	if to > core.MaxSupportedRepoFormat {
		return fmt.Errorf(
			"refusing to stamp repository format %d: this build supports up to %d",
			to, core.MaxSupportedRepoFormat,
		)
	}

	cfg, err := LoadRepoConfig(ctx, rawStore)
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("repository not initialized")
	}
	if cfg.Version >= to {
		return nil
	}

	cfg.Version = to
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal repo config: %w", err)
	}
	if err := rawStore.Put(ctx, "config", data); err != nil {
		return fmt.Errorf("write repo config: %w", err)
	}
	return nil
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
	slots, err := keychain.LoadKeySlots(ctx, rawStore)
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
	slots, err := keychain.LoadKeySlots(ctx, rawStore)
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
	return keychain.ChangePasswordSlot(ctx, rawStore, masterKey, newPassword)
}

// AddRecoveryKeyOptions controls which recovery slot AddRecoveryKey writes.
type AddRecoveryKeyOptions struct {
	// Label names the slot (object key keys/recovery-<label>). Empty means the
	// default slot. Distinct labels let a repository hold several recovery keys,
	// all of which stay valid.
	Label string
	// Replace permits overwriting an existing slot with the same label, which
	// invalidates the mnemonic that slot was issued for.
	Replace bool
}

// AddRecoveryKey generates a BIP39 recovery key for the repository,
// authenticating with kc to obtain the master key.
// Returns the 24-word mnemonic phrase.
//
// If a recovery slot with the requested label already exists and opts.Replace
// is false, it returns a *keychain.SlotExistsError and writes nothing.
func AddRecoveryKey(ctx context.Context, rawStore store.ObjectStore, kc keychain.Chain, opts AddRecoveryKeyOptions) (string, error) {
	if err := requireEncryptedRepo(ctx, rawStore); err != nil {
		return "", err
	}
	slots, err := keychain.LoadKeySlots(ctx, rawStore)
	if err != nil {
		return "", fmt.Errorf("load key slots: %w", err)
	}
	masterKey, err := kc.Resolve(ctx, slots)
	if err != nil {
		return "", fmt.Errorf("unlock repository: %w", err)
	}
	return keychain.AddRecoverySlot(ctx, rawStore, masterKey, opts.Label, opts.Replace)
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

	// Refuse a repository written by a newer build rather than operating on a
	// format we only partly understand. Every path that opens a repository
	// funnels through here, so this is the one gate that has to hold.
	//
	// A version we do not recognise means indexes or objects may be encoded in
	// ways we would misread — and misreading an index as empty is how a prune
	// deletes a live repository. Failing here is the safe outcome.
	if cfg.Version > core.MaxSupportedRepoFormat {
		return nil, fmt.Errorf(
			"repository format version %d is newer than this build supports (up to %d): "+
				"upgrade cloudstic to work with this repository",
			cfg.Version, core.MaxSupportedRepoFormat,
		)
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
	store store.ObjectStore
	// base is the raw, undecorated store. Kept so repository-level markers such
	// as the format version can be written without passing through encryption
	// or packing, which is where init writes them too.
	base store.ObjectStore
	// repoFormat is the in-process view of the on-disk format version, and the
	// single source of truth for format-dependent write policy. The compression
	// layer's frame gate reads it, and raiseRepoFormat is its only writer, so
	// framing cannot disagree with the format a mutation has stamped.
	repoFormat     atomic.Int64
	storedMeter    *store.MeteredStore
	encryptionKey  []byte
	hmacKey        []byte
	keychain       keychain.Chain
	enablePackfile bool
	reporter       ui.Reporter
}

func NewClient(ctx context.Context, base store.ObjectStore, opts ...ClientOption) (*Client, error) {
	c := &Client{
		base:           base,
		enablePackfile: true, // Packfile is enabled by default
		reporter:       ui.NewNoOpReporter(),
	}
	for _, opt := range opts {
		opt(c)
	}

	// Read the config before anything else touches the repository, whether or
	// not a key was supplied. LoadRepoConfig carries the version gate, and
	// gating only the key-resolution path would let a caller bypass it by
	// passing WithEncryptionKey.
	cfg, err := LoadRepoConfig(ctx, base)
	if err != nil {
		return nil, err
	}

	// Auto-detect encryption from the repo config if no explicit key is set.
	if len(c.encryptionKey) == 0 {
		encKey, err := c.resolveKeyFromConfig(ctx, base, cfg)
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
		// PackStore sits below the encryption layer, so the catalog and pack
		// footers it writes never pass through EncryptedStore. Give it its own
		// derived key so they are not left in plaintext.
		var packOpts []store.PackOption
		if len(c.encryptionKey) > 0 {
			indexKey, err := crypto.DeriveKey(c.encryptionKey, crypto.HKDFInfoPackIndexV1)
			if err != nil {
				return nil, fmt.Errorf("derive pack index key: %w", err)
			}
			packOpts = append(packOpts, store.WithPackIndexKey(indexKey))

		}

		packStore, err := store.NewPackStore(inner, packOpts...)
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

	// Seed the in-process format from disk, then let the compression layer read
	// it through the gate. Framing is thus derived from the format, not a
	// separate switch: a mutation raises the format (raiseRepoFormat) and every
	// subsequent write frames, with nothing to keep in sync. A repository below
	// core.FramedCompressionFormat starts unframed and is raised before its
	// first framed write.
	if cfg != nil {
		c.repoFormat.Store(int64(cfg.Version))
	}
	c.store = store.NewCompressedStore(inner, store.WithFrameGate(c.framingEnabled))
	c.storedMeter = storedMeter
	return c, nil
}

// framingEnabled reports whether writes should be framed at the repository's
// current recorded format. It is the gate the compression layer consults.
func (c *Client) framingEnabled() bool {
	return c.repoFormat.Load() >= core.FramedCompressionFormat
}

// resolveKeyFromConfig uses the Keychain to resolve the master key and derive
// the encryption key, for a repository the caller has already loaded the config
// for. It takes cfg rather than re-reading it so that the version gate in
// LoadRepoConfig runs exactly once per client, on every path.
func (c *Client) resolveKeyFromConfig(ctx context.Context, base store.ObjectStore, cfg *RepoConfig) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("repository not initialized -- run 'cloudstic init' first")
	}
	if !cfg.Encrypted {
		return nil, nil
	}
	slots, err := keychain.LoadKeySlots(ctx, base)
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
type ProfilesConfig = engine.ProfilesConfig
type ProfileStore = engine.ProfileStore
type ProfileAuth = engine.ProfileAuth
type BackupProfile = engine.BackupProfile
type DiscoveredSource = engine.DiscoveredSource
type WorkstationSetupPlan = engine.WorkstationSetupPlan
type WorkstationSetupOption = engine.WorkstationSetupOption
type WorkstationApplyResult = engine.WorkstationApplyResult
type WorkstationProfileDraft = engine.WorkstationProfileDraft
type WorkstationFolderCandidate = engine.WorkstationFolderCandidate
type WorkstationCoverageSummary = engine.WorkstationCoverageSummary

var (
	WithVerbose             = engine.WithVerbose
	WithBackupDryRun        = engine.WithBackupDryRun
	WithIgnoreEmptySnapshot = engine.WithIgnoreEmptySnapshot
	WithTags                = engine.WithTags
	WithGenerator           = engine.WithGenerator
	WithMeta                = engine.WithMeta
	WithExcludeHash         = engine.WithExcludeHash
	WithWorkstationProfiles = engine.WithWorkstationProfiles
	WithWorkstationStoreRef = engine.WithWorkstationStoreRef
)

// raiseRepoFormat stamps the on-disk format and updates the in-process view in
// lockstep, so format-dependent write policy — framing — follows immediately.
// It is the only writer of c.repoFormat.
//
// Raising is a write, so it obeys the "writes stamp, reads do not" rule: a read
// that silently changed the repository would be a surprising side effect of an
// innocuous command, and would lock out a machine that was only listing
// snapshots. A newer build having *written* here is a real signal; a newer
// build having *looked* is not.
func (c *Client) raiseRepoFormat(ctx context.Context) error {
	if c.base == nil {
		return nil
	}
	if err := UpgradeRepoFormat(ctx, c.base, core.RepoFormatVersion); err != nil {
		return err
	}
	c.repoFormat.Store(int64(core.RepoFormatVersion))
	return nil
}

// stampWriteFormat raises the format after a successful mutation, best-effort:
// the data is already written and the next mutation stamps again, so a failure
// is logged rather than surfaced.
//
// This is right for prune and forget. What they write through the compression
// layer is JSON — snapshot and index manifests — which always compresses, so an
// unframed one is decoded correctly by the legacy read path and nothing is lost
// by stamping afterwards. Backup is the exception: it stamps *before* writing
// (see its comment), because it stores user file content, the one thing an
// unframed write corrupts permanently.
func (c *Client) stampWriteFormat(ctx context.Context) {
	if err := c.raiseRepoFormat(ctx); err != nil {
		log.Debugf("could not stamp repository format: %v", err)
	}
}

func (c *Client) Backup(ctx context.Context, src source.Source, opts ...BackupOption) (*BackupResult, error) {
	// Raise the format before writing anything, and fail if it cannot be raised.
	//
	// Backup stores user file content, which may be incompressible and may begin
	// with a magic header — the combination the frame exists for. An object this
	// build writes unframed is unframed permanently: content-addressed objects
	// are never rewritten, so a later framed backup skips it on the Exists check
	// and nothing repairs it. Stamping afterwards, as prune and forget do, would
	// mean every repository below the framing format lost its already-compressed
	// files on the first backup after upgrading. Raising first makes framing
	// (which follows the format) already on by the time the first object is
	// written; continuing on a raise error would write exactly those unframed
	// objects, so the error is fatal.
	if err := c.raiseRepoFormat(ctx); err != nil {
		return nil, fmt.Errorf("raise repository format before writing: %w", err)
	}

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

func (c *Client) DiscoverSources(ctx context.Context) ([]DiscoveredSource, error) {
	return engine.DiscoverSources(ctx)
}

func PlanWorkstationSetup(ctx context.Context, opts ...WorkstationSetupOption) (*WorkstationSetupPlan, error) {
	return engine.PlanWorkstationSetup(ctx, opts...)
}

func ApplyWorkstationSetupPlan(cfg *ProfilesConfig, plan *WorkstationSetupPlan) (*WorkstationApplyResult, error) {
	return engine.ApplyWorkstationSetupPlan(cfg, plan)
}

// LoadProfilesFile parses a backup profiles YAML file.
func LoadProfilesFile(path string) (*ProfilesConfig, error) {
	return engine.LoadProfilesFile(path)
}

// SaveProfilesFile writes a backup profiles YAML file.
func SaveProfilesFile(path string, cfg *ProfilesConfig) error {
	return engine.SaveProfilesFile(path, cfg)
}

// LoadProfilesFileOrEmpty loads profiles from path, treating a missing file
// as an empty, version-1 config rather than an error.
func LoadProfilesFileOrEmpty(path string) (*ProfilesConfig, error) {
	return engine.LoadProfilesFileOrEmpty(path)
}

// EnsureProfilesMaps guarantees cfg's map fields are non-nil, so callers can
// write into them unconditionally.
func EnsureProfilesMaps(cfg *ProfilesConfig) {
	engine.EnsureProfilesMaps(cfg)
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

type RestoreOption = engine.RestoreOption
type RestoreResult = engine.RestoreResult

var (
	WithRestoreDryRun   = engine.WithRestoreDryRun
	WithRestoreVerbose  = engine.WithRestoreVerbose
	WithRestorePath     = engine.WithRestorePath
	WithRestoreNoVerify = engine.WithRestoreNoVerify
)

// Restore writes the snapshot's file tree as a ZIP archive to w.
// snapshotRef can be "", "latest", a bare hash, or "snapshot/<hash>".
func (c *Client) Restore(ctx context.Context, w io.Writer, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	mgr := engine.NewRestoreManager(c.store, c.reporter)
	return mgr.Run(ctx, engine.NewZipRestoreWriter(w), snapshotRef, opts...)
}

// RestoreToDir writes the snapshot's file tree directly into outputDir.
// snapshotRef can be "", "latest", a bare hash, or "snapshot/<hash>".
func (c *Client) RestoreToDir(ctx context.Context, outputDir, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	mgr := engine.NewRestoreManager(c.store, c.reporter)
	writer, err := engine.NewFSRestoreWriter(outputDir)
	if err != nil {
		return nil, err
	}
	return mgr.Run(ctx, writer, snapshotRef, opts...)
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
	result, err := mgr.Run(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if !result.DryRun {
		c.stampWriteFormat(ctx)
	}
	return result, nil
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
	result, err := mgr.Run(ctx, snapshotID, opts...)
	if err != nil {
		return nil, err
	}
	if !result.DryRun {
		c.stampWriteFormat(ctx)
	}
	return result, nil
}

func (c *Client) ForgetPolicy(ctx context.Context, opts ...ForgetOption) (*PolicyResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	result, err := mgr.RunPolicy(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if !result.DryRun {
		c.stampWriteFormat(ctx)
	}
	return result, nil
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
