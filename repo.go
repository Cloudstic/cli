package cloudstic

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/repoconfig"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/store"
)

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
