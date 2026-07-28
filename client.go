package cloudstic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/repoconfig"
	"github.com/cloudstic/cli/internal/secretref"
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
//
// encryptionKey is required for an encrypted repository, whose marker is
// sealed: the version lives inside the sealed blob, so raising it means
// unsealing and resealing. Pass nil for an unencrypted repository.
func UpgradeRepoFormat(
	ctx context.Context,
	rawStore store.ObjectStore,
	to int,
	encryptionKey []byte,
) error {
	if to > core.MaxSupportedRepoFormat {
		return fmt.Errorf(
			"refusing to stamp repository format %d: this build supports up to %d",
			to, core.MaxSupportedRepoFormat,
		)
	}

	cfg, err := LoadRepoConfig(ctx, rawStore, encryptionKey)
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
	return putRepoConfig(ctx, rawStore, *cfg, encryptionKey)
}

// putRepoConfig encodes and stores the marker, sealing it for an encrypted
// repository. Every write of the marker goes through here, so a repository
// that was sealed cannot be left in plaintext by a path that forgot to seal.
func putRepoConfig(
	ctx context.Context,
	rawStore store.ObjectStore,
	cfg RepoConfig,
	encryptionKey []byte,
) error {
	data, err := repoconfig.Encode(cfg, encryptionKey)
	if err != nil {
		return err
	}
	if err := rawStore.Put(ctx, "config", data); err != nil {
		return fmt.Errorf("write repo config: %w", err)
	}
	return nil
}

// requireEncryptedRepo loads the repository config and returns an error if
// the repository has not been initialized or does not use encryption.
func requireEncryptedRepo(ctx context.Context, rawStore store.ObjectStore) error {
	// InspectRepo, not LoadRepoConfig: these callers are on their way to
	// unlocking the repository, so they cannot need the key in order to ask
	// whether one is required.
	status, err := InspectRepo(ctx, rawStore)
	if err != nil {
		return fmt.Errorf("read repository config: %w", err)
	}
	if !status.Initialized {
		return fmt.Errorf("repository not initialized -- run 'cloudstic init' first")
	}
	if !status.Encrypted {
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

// fetchRepoConfigBytes returns the raw marker, or (nil, nil) if the repository
// has not been initialized yet.
//
// A single Get, not Exists-then-Get. Get already distinguishes the two outcomes
// this has to tell apart — every backend wraps store.ErrNotFound for a missing
// key and returns anything else verbatim — so the probe was a second round trip
// that answered a question the Get answers on its own. This runs on the open
// path of every command, where on a high-latency backend each avoided round
// trip is directly visible.
func fetchRepoConfigBytes(ctx context.Context, rawStore store.ObjectStore) ([]byte, error) {
	data, err := rawStore.Get(ctx, "config")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil // repository not initialized
		}
		return nil, fmt.Errorf("read repo config: %w", err)
	}
	return data, nil
}

// checkRepoFormatSupported refuses a repository written by a newer build rather
// than operating on a format we only partly understand.
//
// A version we do not recognise means indexes or objects may be encoded in ways
// we would misread — and misreading an index as empty is how a prune deletes a
// live repository. Failing here is the safe outcome.
//
// Sealing moves this check after the key is resolved, because the version now
// lives inside the sealed marker. A repository written by a newer build
// therefore asks for credentials before it can report that its format is
// unsupported; every path that decodes a marker still funnels through here.
func checkRepoFormatSupported(cfg *RepoConfig) error {
	if cfg.Version > core.MaxSupportedRepoFormat {
		return fmt.Errorf(
			"repository format version %d is newer than this build supports (up to %d): "+
				"upgrade cloudstic to work with this repository",
			cfg.Version, core.MaxSupportedRepoFormat,
		)
	}
	return nil
}

// RepoStatus is what can be determined about a repository without resolving its
// encryption key.
type RepoStatus struct {
	// Initialized reports whether a config marker exists at all.
	Initialized bool
	// Encrypted reports whether the repository uses encryption. A sealed marker
	// answers this on its own: only an encrypted repository has a key to seal
	// with.
	Encrypted bool
	// Sealed reports whether the marker itself is sealed. An encrypted
	// repository written before sealing existed is Encrypted but not Sealed.
	Sealed bool
}

// InspectRepo reports what can be learned about a repository without its key.
//
// This exists for callers that only need to know whether a repository is
// initialized or encrypted — deciding whether to prompt for credentials, for
// instance — which sealing would otherwise make impossible to answer without
// first doing the very unlock the caller is trying to decide about.
func InspectRepo(ctx context.Context, rawStore store.ObjectStore) (RepoStatus, error) {
	raw, err := fetchRepoConfigBytes(ctx, rawStore)
	if err != nil {
		return RepoStatus{}, err
	}
	if raw == nil {
		return RepoStatus{}, nil
	}
	if repoconfig.IsSealed(raw) {
		return RepoStatus{Initialized: true, Encrypted: true, Sealed: true}, nil
	}
	cfg, err := repoconfig.Decode(raw, nil)
	if err != nil {
		return RepoStatus{}, err
	}
	return RepoStatus{Initialized: true, Encrypted: cfg.Encrypted}, nil
}

// LoadRepoConfig reads the repository marker from a raw (undecorated) store.
// Returns (nil, nil) if the repository has not been initialized yet.
// Returns an error if the store is unreachable (e.g. invalid credentials).
//
// encryptionKey is required when the marker is sealed and ignored otherwise.
// Callers that only need to know whether a repository is initialized or
// encrypted should use InspectRepo, which needs no key.
func LoadRepoConfig(
	ctx context.Context,
	rawStore store.ObjectStore,
	encryptionKey []byte,
) (*RepoConfig, error) {
	raw, err := fetchRepoConfigBytes(ctx, rawStore)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	cfg, err := repoconfig.Decode(raw, encryptionKey)
	if err != nil {
		return nil, err
	}
	if err := checkRepoFormatSupported(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Re-exported types from internal packages
// ---------------------------------------------------------------------------

// RepoConfig is the repository marker written by "init".
type RepoConfig = core.RepoConfig

// FileMeta represents immutable file metadata, as referenced by
// pkg/source.Source implementations and by result types such as FileMatch
// and LsSnapshotResult.
type FileMeta = core.FileMeta

// SourceInfo describes the origin of a backup snapshot, as referenced by
// pkg/source.Source.Info and by result types such as FileMatch.
type SourceInfo = core.SourceInfo

// FileType is the generic type of a file (file or folder).
type FileType = core.FileType

// FileType values.
const (
	FileTypeFile   = core.FileTypeFile
	FileTypeFolder = core.FileTypeFolder
)

// Snapshot represents a backup checkpoint, as referenced by LsSnapshotResult.
type Snapshot = core.Snapshot

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
	repoFormat atomic.Int64
	// openCfg is the config NewClient read, held so the first raiseRepoFormat
	// of this client does not immediately re-read it. It is consumed on first
	// use (swapped for the noRepoConfig sentinel) and never consulted again.
	openCfg        atomic.Pointer[RepoConfig]
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
	// not a key was supplied. This carries the version gate, and gating only
	// the key-resolution path would let a caller bypass it by passing
	// WithEncryptionKey.
	cfg, encKey, err := c.openRepoConfig(ctx, base)
	if err != nil {
		return nil, err
	}
	c.encryptionKey = encKey

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
		c.openCfg.Store(cfg)
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
func (c *Client) resolveKeyFromSlots(ctx context.Context, base store.ObjectStore) ([]byte, error) {
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

// openRepoConfig reads the marker and resolves the encryption key in one step,
// because neither can be done first in general: a sealed marker cannot be
// decoded until the key is resolved, and whether a key is needed at all is
// what the marker says.
//
// The marker's own form breaks the cycle. Sealed means encrypted, so the key
// slots can be opened without consulting the config; plaintext means the
// config is readable directly, and its "encrypted" field then decides.
//
// An explicitly supplied key (WithEncryptionKey) is used as-is and suppresses
// slot resolution, but never the version gate.
func (c *Client) openRepoConfig(
	ctx context.Context,
	base store.ObjectStore,
) (*RepoConfig, []byte, error) {
	raw, err := fetchRepoConfigBytes(ctx, base)
	if err != nil {
		return nil, nil, err
	}
	if raw == nil {
		return nil, c.encryptionKey, nil // not initialized
	}

	key := c.encryptionKey
	sealed := repoconfig.IsSealed(raw)
	if sealed && len(key) == 0 {
		if key, err = c.resolveKeyFromSlots(ctx, base); err != nil {
			return nil, nil, err
		}
	}

	cfg, err := repoconfig.Decode(raw, key)
	if err != nil {
		return nil, nil, err
	}
	if err := checkRepoFormatSupported(cfg); err != nil {
		return nil, nil, err
	}

	if !sealed {
		// A plaintext marker claiming no encryption, on a repository that has
		// key slots, is a contradiction: the slots exist only to unwrap a key
		// this claims does not exist. Flipping that one field is otherwise the
		// cheapest way to make a client read an encrypted repository as
		// plaintext, so refuse it here — the check needs no key.
		if !cfg.Encrypted && keychain.HasKeySlots(ctx, base) {
			return nil, nil, fmt.Errorf(
				"repository config says encryption is disabled but key slots exist: " +
					"the config marker may have been modified; restore its original version",
			)
		}
		if cfg.Encrypted && len(key) == 0 {
			if key, err = c.resolveKeyFromSlots(ctx, base); err != nil {
				return nil, nil, err
			}
		}
	}

	return cfg, key, nil
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

// noRepoConfig marks Client.openCfg as spent. A plain nil cannot: nil is also
// what the field holds for an uninitialized repository, and those two states
// have to stay distinguishable.
var noRepoConfig RepoConfig

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

	// The first raise can answer from the config NewClient just read, instead
	// of fetching "config" a second time within the same command — two round
	// trips to read one small immutable object, back to back, which is
	// plainly visible on a high-latency backend.
	//
	// Only the first. The cached copy is consumed here and every later raise
	// re-reads, because "already current" then stops being a safe assumption:
	// a long-lived client outlives the moment it was opened, and the on-disk
	// version is what other machines act on. Skipping the write on a stale
	// in-process belief would leave a repository unstamped after a real
	// mutation — see TestDryRunsDoNotStampTheFormat.
	if cfg := c.openCfg.Swap(&noRepoConfig); cfg != nil && cfg != &noRepoConfig {
		if cfg.Version >= core.RepoFormatVersion {
			c.repoFormat.Store(int64(core.RepoFormatVersion))
			return nil
		}
	}

	if err := UpgradeRepoFormat(ctx, c.base, core.RepoFormatVersion, c.encryptionKey); err != nil {
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

// SecretRefError reports a malformed scheme://path secret reference (e.g. in
// one of a profile's *_secret fields). Use errors.As to inspect it and Kind
// to branch on the failure mode.
type SecretRefError = secretref.Error

// SecretRefErrorKind categorizes a SecretRefError.
type SecretRefErrorKind = secretref.ErrorKind

const (
	SecretRefInvalid            = secretref.KindInvalidRef
	SecretRefNotFound           = secretref.KindNotFound
	SecretRefBackendUnavailable = secretref.KindBackendUnavailable
)

// SecretResolver resolves a scheme://path secret reference to its value, as
// accepted by pkg/source/onedrive.WithResolver and pkg/source/gdrive.WithResolver.
type SecretResolver = secretref.Resolver

// SecretRef is a parsed scheme://path secret reference.
type SecretRef = secretref.Ref

// WritableSecretBackend is a secret backend that supports writing new values,
// as returned by SecretResolver.WritableBackends.
type WritableSecretBackend = secretref.WritableBackend

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
	// ErrSnapshotNotFound means no snapshot matched a requested reference.
	ErrSnapshotNotFound = engine.ErrSnapshotNotFound
	// ErrSnapshotRefAmbiguous means more than one snapshot matched a hash prefix.
	ErrSnapshotRefAmbiguous = engine.ErrSnapshotRefAmbiguous

	WithRestoreDryRun   = engine.WithRestoreDryRun
	WithRestoreVerbose  = engine.WithRestoreVerbose
	WithRestorePath     = engine.WithRestorePath
	WithRestoreNoVerify = engine.WithRestoreNoVerify
)

// Restore writes the snapshot's file tree as a ZIP archive to w.
// snapshotRef can be "", "latest", a bare hash or unambiguous hash prefix, or
// "snapshot/<hash-or-prefix>". An ambiguous prefix is rejected.
func (c *Client) Restore(ctx context.Context, w io.Writer, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	mgr := engine.NewRestoreManager(c.store, c.reporter)
	return mgr.Run(ctx, engine.NewZipRestoreWriter(w), snapshotRef, opts...)
}

// RestoreToDir writes the snapshot's file tree directly into outputDir.
// snapshotRef can be "", "latest", a bare hash or unambiguous hash prefix, or
// "snapshot/<hash-or-prefix>". An ambiguous prefix is rejected.
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

// LsSnapshot lists a snapshot selected by latest, full hash, or unambiguous
// hash prefix. An ambiguous prefix is rejected.
func (c *Client) LsSnapshot(ctx context.Context, snapshotID string, opts ...LsSnapshotOption) (*LsSnapshotResult, error) {
	mgr := engine.NewLsSnapshotManager(c.store)
	return mgr.Run(ctx, snapshotID, opts...)
}

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------

type FindOption = engine.FindOption
type FindQuery = engine.FindQuery
type FindResult = engine.FindResult
type FileMatch = engine.FileMatch
type FileVersion = engine.FileVersion
type SnapshotRef = engine.SnapshotRef
type SizeCompare = engine.SizeCompare
type SizeOp = engine.SizeOp

const (
	SizeAtLeast = engine.SizeAtLeast
	SizeAtMost  = engine.SizeAtMost
	SizeExactly = engine.SizeExactly
)

var (
	WithFindPattern        = engine.WithFindPattern
	WithFindName           = engine.WithFindName
	WithFindPath           = engine.WithFindPath
	WithFindRegex          = engine.WithFindRegex
	WithFindIgnoreCase     = engine.WithFindIgnoreCase
	WithFindFileID         = engine.WithFindFileID
	WithFindContentHash    = engine.WithFindContentHash
	WithFindRef            = engine.WithFindRef
	WithFindType           = engine.WithFindType
	WithFindSize           = engine.WithFindSize
	WithFindNewer          = engine.WithFindNewer
	WithFindOlder          = engine.WithFindOlder
	WithFindSnapshots      = engine.WithFindSnapshots
	WithFindSource         = engine.WithFindSource
	WithFindTags           = engine.WithFindTags
	WithFindLatest         = engine.WithFindLatest
	WithFindSince          = engine.WithFindSince
	WithFindUntil          = engine.WithFindUntil
	WithFindGroupByContent = engine.WithFindGroupByContent
	WithFindMaxResults     = engine.WithFindMaxResults
	WithFindNoDelta        = engine.WithFindNoDelta
	WithFindVerbose        = engine.WithFindVerbose

	ParseSizeCompare = engine.ParseSizeCompare
	ParseFindTime    = engine.ParseFindTime
)

// Find locates files across the repository's snapshots without the caller
// having to know which snapshot holds them.
//
// Unlike every other read operation, Find takes a snapshot as *output* rather
// than input: it searches every snapshot by default, and reports for each
// matching file the versions it has had and the snapshots each version lives in.
//
// It is a pure read path — no lock is taken, nothing is written, and the
// repository format is not stamped.
func (c *Client) Find(ctx context.Context, opts ...FindOption) (*FindResult, error) {
	mgr := engine.NewFindManager(c.store)
	return mgr.Run(ctx, opts...)
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

// PolicyGroupResult holds the policy evaluation result for a single group of
// snapshots, as returned in PolicyResult.Groups.
type PolicyGroupResult = engine.PolicyGroupResult

// GroupKey identifies a group of snapshots for policy application.
type GroupKey = engine.GroupKey

// KeepReason pairs a snapshot with the reasons it was kept.
type KeepReason = engine.KeepReason

// SnapshotEntry is a snapshot loaded for policy evaluation, as referenced by
// KeepReason and by ListResult.Snapshots.
type SnapshotEntry = engine.SnapshotEntry

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

// ErrRepoLocked means Backup, Restore, or Prune could not proceed because the
// repository is held by another operation. Use errors.Is(err, ErrRepoLocked)
// to detect the condition and prompt the caller toward BreakLock.
var ErrRepoLocked = engine.ErrRepoLocked

func (c *Client) BreakLock(ctx context.Context) ([]*RepoLock, error) {
	return engine.BreakRepoLock(ctx, c.store)
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

type DiffOption = engine.DiffOption
type DiffResult = engine.DiffResult

// FileChange is one change reported by Diff, between two snapshots.
type FileChange = engine.FileChange

// ChangeType describes the kind of change a FileChange represents.
type ChangeType = engine.ChangeType

const (
	ChangeAdded    = engine.ChangeAdded
	ChangeRemoved  = engine.ChangeRemoved
	ChangeModified = engine.ChangeModified
)

var WithDiffVerbose = engine.WithDiffVerbose

// Diff compares snapshots selected by latest, full hashes, or unambiguous hash
// prefixes. An ambiguous prefix is rejected.
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
