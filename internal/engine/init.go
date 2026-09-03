package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/repoconfig"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/store"
)

var defaultInitLog = logger.New("init", logger.ColorYellow)

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

// InitOption configures an init operation.
type InitOption func(*initConfig)

type initConfig struct {
	chain        keychain.Chain
	recovery     bool
	noEncryption bool
	adoptSlots   bool
	format       int // 0 means the default, core.RepoFormatVersion
}

// WithInitCredentials configures the keychain to use for initialization.
func WithInitCredentials(chain keychain.Chain) InitOption {
	return func(cfg *initConfig) { cfg.chain = chain }
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// InitResult holds the outcome of an init operation.
type InitResult struct {
	Encrypted    bool
	AdoptedSlots bool   // true if existing key slots were adopted
	RecoveryKey  string // BIP39 24-word mnemonic; empty if not requested
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// InitManager bootstraps a new repository: creates encryption key slots and
// writes the "config" marker.
type InitManager struct {
	store store.ObjectStore
	// log is this manager's debug sink; an unbound logger falls back to the
	// process-wide writer.
	log *logger.Logger
}

// NewInitManager creates an InitManager that operates on the raw (undecorated)
// object store.
func NewInitManager(d Deps) *InitManager {
	return &InitManager{store: d.Store, log: defaultInitLog.To(d.LogSink)}
}

const configKey = "config"

// Run executes the init operation.
func (m *InitManager) Run(ctx context.Context, opts ...InitOption) (*InitResult, error) {
	var cfg initConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	// Whether the caller named a format. cfg.format stops being able to say so
	// once the default is resolved, and one of the two adopt paths — the
	// sealed marker, whose version cannot be read until the key exists — runs
	// after that point.
	formatRequested := cfg.format != 0

	// The only formats an init may mint. Validated before the default is
	// resolved, so that an explicit value is rejected on its own terms.
	if cfg.format != 0 && cfg.format != core.RepoFormatV2 && cfg.format != core.RepoFormatV3 {
		return nil, fmt.Errorf("unsupported repository format %d: this build creates format %d or %d (default)",
			cfg.format, core.RepoFormatV2, core.RepoFormatV3)
	}
	// Check if already initialized. A read failure that is not "no config yet"
	// must abort rather than fall through as if this were a fresh repository —
	// otherwise a transient store error could make init overwrite an existing
	// repository's key slots and config.
	cfgData, err := m.store.Get(ctx, configKey)
	switch {
	case err == nil && cfgData != nil:
		if !cfg.adoptSlots {
			return nil, fmt.Errorf("repository is already initialized")
		}
		// Adopting an existing repository reads its marker directly rather
		// than through LoadRepoConfig, so the version gate does not apply
		// here. Apply it: adopting a repository whose format this build cannot
		// read would rewrite its marker to a version this build understands
		// while leaving data it does not — turning a repository that fails
		// safely into one that is silently misread.
		//
		// A sealed marker cannot be decoded here — the key is not resolved
		// until setupEncryption below — so its gate runs there instead, once
		// the key exists. checkEncryptionInPlace needs no such deferral: a
		// sealed marker means an already-encrypted repository, which is the
		// case that check returns on immediately.
		var existing core.RepoConfig
		if err := json.Unmarshal(cfgData, &existing); err == nil {
			if err := checkAdoptedRepoFormat(existing.Version); err != nil {
				return nil, err
			}
			// Adopting keeps the repository's own format. The build default
			// is what a *new* repository gets; applying it here would try to
			// re-initialize an existing packfile repository as v3, which the
			// format check below refuses outright — correctly, since the
			// stored structures do not change with the marker. An explicit
			// -format is still honoured, and still refused if it disagrees
			// with what is there.
			if !formatRequested {
				cfg.format = existing.Version
			}
			if err := m.checkEncryptionInPlace(ctx, existing, cfg); err != nil {
				return nil, err
			}
		}
	case err == nil, errors.Is(err, store.ErrNotFound):
		// Not yet initialized. (Some ObjectStore test doubles return (nil,
		// nil) rather than ErrNotFound for a missing key; a real backend
		// never does, but treating a nil error as "not found" here is
		// harmless either way.)
	default:
		return nil, fmt.Errorf("check for existing repository: %w", err)
	}

	if cfg.format == 0 {
		cfg.format = core.RepoFormatVersion
	}
	m.log.Debugf("InitRepo: encrypted=%v, noEncryption=%v, adoptSlots=%v, hasChain=%v, recovery=%v, format=%d",
		!cfg.noEncryption && len(cfg.chain) > 0, cfg.noEncryption, cfg.adoptSlots, len(cfg.chain) > 0, cfg.recovery, cfg.format)

	hasCreds := len(cfg.chain) > 0
	encrypted := hasCreds && !cfg.noEncryption
	result := &InitResult{Encrypted: encrypted}
	var encryptionKey []byte

	if encrypted {
		adopted, key, err := m.setupEncryption(ctx, cfg)
		if err != nil {
			return nil, err
		}
		result.AdoptedSlots = adopted
		encryptionKey = key

		// The deferred half of the adopt-time version gate, for a marker that
		// was sealed and so could not be read before the key existed.
		if len(cfgData) > 0 && repoconfig.IsSealed(cfgData) {
			existing, err := repoconfig.Decode(cfgData, encryptionKey)
			if err != nil {
				return nil, err
			}
			if err := checkAdoptedRepoFormat(existing.Version); err != nil {
				return nil, err
			}
			// Adopting keeps the repository's own format. The build default
			// is what a *new* repository gets; applying it here would try to
			// re-initialize an existing packfile repository as v3, which the
			// format check below refuses outright — correctly, since the
			// stored structures do not change with the marker. An explicit
			// -format is still honoured, and still refused if it disagrees
			// with what is there.
			if !formatRequested {
				cfg.format = existing.Version
			}
		}

		if cfg.recovery {
			mnemonic, err := m.addRecoverySlot(ctx, cfg)
			if err != nil {
				return nil, err
			}
			result.RecoveryKey = mnemonic
		}
	}

	if err := m.writeRepoConfig(ctx, cfg.format, encrypted, encryptionKey); err != nil {
		return nil, err
	}

	return result, nil
}

// checkAdoptedRepoFormat applies the version gate to a repository being
// adopted. Adoption rewrites the marker, so a repository whose format this
// build cannot read would be restamped to a version this build understands
// while leaving data it does not — turning a repository that fails safely into
// one that is silently misread.
func checkAdoptedRepoFormat(version int) error {
	if version > core.MaxSupportedRepoFormat {
		return fmt.Errorf(
			"cannot adopt repository: format version %d is newer than this build supports (up to %d): "+
				"upgrade cloudstic to work with this repository",
			version, core.MaxSupportedRepoFormat,
		)
	}
	return nil
}

// checkEncryptionInPlace refuses to turn an existing unencrypted repository
// that already holds data into an encrypted one.
//
// Adoption used to allow it, and what it produced was a repository recording
// `encrypted: true` whose existing objects were all plaintext — readable only
// because EncryptedStore returned anything that was not a ciphertext as-is.
// That fallback is exactly the hole an attacker with write access to the
// backing store walks through: a client holding the correct key would accept
// those objects, and any forged object indistinguishable from them, as
// genuine repository content.
//
// Converting in place also never encrypted anything already stored, so
// refusing costs no confidentiality. Backing the data up into a repository
// that was created encrypted does, and that is what the message points at.
func (m *InitManager) checkEncryptionInPlace(ctx context.Context, existing core.RepoConfig, cfg initConfig) error {
	if existing.Encrypted || cfg.noEncryption || len(cfg.chain) == 0 {
		return nil
	}
	populated, err := m.holdsObjects(ctx)
	if err != nil {
		return err
	}
	if !populated {
		// Nothing stored yet, so nothing to strand: an empty repository created
		// with -no-encryption can still change its mind.
		return nil
	}
	return fmt.Errorf(
		"cannot encrypt this repository in place: it already holds unencrypted backups, which " +
			"enabling encryption would neither encrypt nor keep readable as repository content. " +
			"Initialize a new encrypted repository and back up into it instead",
	)
}

// holdsObjects reports whether the repository stores backup data, as opposed to
// only a config marker and key slots. Snapshots may be bundled into packfiles,
// so both namespaces count.
func (m *InitManager) holdsObjects(ctx context.Context) (bool, error) {
	for _, prefix := range []string{"snapshot/", "packs/"} {
		keys, err := m.store.List(ctx, prefix)
		if err != nil {
			return false, fmt.Errorf("inspect repository contents: %w", err)
		}
		if len(keys) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// setupEncryption creates new key slots or adopts existing ones. It reports
// whether existing slots were adopted, and returns the repository encryption
// key, which the caller needs to seal the config marker it then writes.
func (m *InitManager) setupEncryption(
	ctx context.Context,
	cfg initConfig,
) (adopted bool, encryptionKey []byte, err error) {
	slots, err := keychain.LoadKeySlots(ctx, m.store)
	if err != nil {
		return false, nil, fmt.Errorf("load key slots: %w", err)
	}

	var masterKey []byte
	if len(slots) > 0 {
		// Use existing master key.
		mk, err := cfg.chain.Resolve(ctx, slots)
		if err != nil {
			return false, nil, fmt.Errorf("found existing key slots but cannot open them: %w", err)
		}
		masterKey = mk
		adopted = true
	} else {
		// Generate new master key.
		mk, err := crypto.GenerateKey()
		if err != nil {
			return false, nil, fmt.Errorf("generate master key for init: %w", err)
		}
		masterKey = mk
	}

	encryptionKey, err = keychain.DeriveEncryptionKey(masterKey)
	if err != nil {
		return false, nil, fmt.Errorf("derive encryption key: %w", err)
	}

	// Always wrap and write slots in the provided chain.
	// This ensures that new credentials provided during 'adopt' get their own slots.
	newSlots, err := cfg.chain.WrapAll(ctx, masterKey)
	if err != nil {
		return false, nil, fmt.Errorf("wrap master key: %w", err)
	}
	for _, slot := range newSlots {
		if err := keychain.WriteKeySlot(ctx, m.store, slot); err != nil {
			return false, nil, fmt.Errorf("write key slot: %w", err)
		}
	}

	return adopted, encryptionKey, nil
}

// addRecoverySlot extracts the master key and creates a recovery slot.
func (m *InitManager) addRecoverySlot(ctx context.Context, cfg initConfig) (string, error) {
	slots, err := keychain.LoadKeySlots(ctx, m.store)
	if err != nil {
		return "", fmt.Errorf("reload key slots: %w", err)
	}
	masterKey, err := cfg.chain.Resolve(ctx, slots)
	if err != nil {
		return "", fmt.Errorf("extract master key for recovery slot: %w", err)
	}
	// The repository is being created here, so there is no earlier mnemonic to
	// invalidate: write the default slot unconditionally.
	mnemonic, err := keychain.AddRecoverySlot(ctx, m.store, masterKey, keychain.DefaultSlotLabel, true)
	if err != nil {
		return "", fmt.Errorf("create recovery key: %w", err)
	}
	return mnemonic, nil
}

// writeRepoConfig writes the repository marker, sealing it with encryptionKey
// when the repository is encrypted. encryptionKey must be non-empty in that
// case; it is ignored otherwise.
func (m *InitManager) writeRepoConfig(ctx context.Context, formatVersion int, encrypted bool, encryptionKey []byte) error {
	// The recorded format is a floor and must never move down. Adopting an
	// existing repository rewrites this marker, and stamping a lower version
	// than the repository has already reached would advertise it as readable by
	// builds that would misread it. A transient read failure here must not be
	// mistaken for "no existing config" — that would silently move the floor
	// down instead of leaving it alone.
	version := formatVersion
	id := ""
	existingData, err := m.store.Get(ctx, configKey)
	switch {
	case err == nil:
		// Decode rather than unmarshal, so a sealed marker's version is read
		// too. A marker that cannot be opened leaves the floor where it is,
		// which is the same conservative outcome as an unparseable one.
		if existing, derr := repoconfig.Decode(existingData, encryptionKey); derr == nil {
			// Within the packfile family (formats ≤ 2) the marker may move up:
			// that is the ordinary in-place upgrade, a claim about minimum
			// reader version over structures every such build reads. Crossing
			// into v3 is different in kind — v3 names a repository whose
			// entries live in leaf payloads, which no marker rewrite can make
			// true of existing objects. Only migration crosses that line
			// (RFC 0026).
			if version >= core.RepoFormatV3 && existing.Version < core.RepoFormatV3 {
				return fmt.Errorf(
					"cannot re-initialize a format-%d repository as format %d: "+
						"the stored structures do not change with the marker; migrate instead",
					existing.Version, version,
				)
			}
			if existing.Version > version {
				version = existing.Version
			}
			// Adopting a repository must not re-identify it. Snapshots copied
			// out of it elsewhere record this identifier as their provenance,
			// and minting a fresh one would make those copies look unrelated —
			// so the next `copy` would import the whole history again.
			id = existing.ID
		}
	case !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("read existing repository config: %w", err)
	}

	// A repository predating RepoConfig.ID reaches here with id still empty
	// only when its marker could not be decoded, in which case it is being
	// re-initialized anyway. An adopted repository that genuinely has no
	// identifier gains one here, which is safe: it had no provenance to
	// invalidate.
	if id == "" {
		if id, err = core.NewRepoID(); err != nil {
			return err
		}
	}

	cfg := core.RepoConfig{
		Version:   version,
		Created:   time.Now().UTC().Format(time.RFC3339),
		Encrypted: encrypted,
		ID:        id,
	}
	data, err := repoconfig.Encode(cfg, encryptionKey)
	if err != nil {
		return err
	}
	return m.store.Put(ctx, configKey, data)
}

// WithInitRecovery requests generation of a recovery key during init.
func WithInitRecovery() InitOption {
	return func(cfg *initConfig) { cfg.recovery = true }
}

// WithInitNoEncryption creates an unencrypted repository.
func WithInitNoEncryption() InitOption {
	return func(cfg *initConfig) { cfg.noEncryption = true }
}

// WithInitAdoptSlots allows initialization to succeed even if key slots already exist.
func WithInitAdoptSlots() InitOption {
	return func(cfg *initConfig) { cfg.adoptSlots = true }
}

// WithInitFormat selects the repository format version to create (RFC 0026).
// Zero means the build's default, core.RepoFormatVersion. Validation happens
// in Run, since an option is a plain mutator.
func WithInitFormat(version int) InitOption {
	return func(cfg *initConfig) { cfg.format = version }
}
