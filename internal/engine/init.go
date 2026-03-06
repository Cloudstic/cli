package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/store"
)

var initLog = logger.New("init", logger.ColorYellow)

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
}

// NewInitManager creates an InitManager that operates on the raw (undecorated)
// object store.
func NewInitManager(s store.ObjectStore) *InitManager {
	return &InitManager{store: s}
}

const configKey = "config"

// Run executes the init operation.
func (m *InitManager) Run(ctx context.Context, opts ...InitOption) (*InitResult, error) {
	var cfg initConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	initLog.Debugf("InitRepo: encrypted=%v, noEncryption=%v, adoptSlots=%v, hasChain=%v, recovery=%v",
		!cfg.noEncryption && len(cfg.chain) > 0, cfg.noEncryption, cfg.adoptSlots, len(cfg.chain) > 0, cfg.recovery)

	// Check if already initialized.
	cfgData, err := m.store.Get(ctx, configKey)
	if err == nil && cfgData != nil {
		if !cfg.adoptSlots {
			return nil, fmt.Errorf("repository is already initialized")
		}
	}

	hasCreds := len(cfg.chain) > 0
	encrypted := hasCreds && !cfg.noEncryption
	result := &InitResult{Encrypted: encrypted}

	if encrypted {
		adopted, err := m.setupEncryption(ctx, cfg)
		if err != nil {
			return nil, err
		}
		result.AdoptedSlots = adopted

		if cfg.recovery {
			mnemonic, err := m.addRecoverySlot(ctx, cfg)
			if err != nil {
				return nil, err
			}
			result.RecoveryKey = mnemonic
		}
	}

	if err := m.writeRepoConfig(ctx, encrypted); err != nil {
		return nil, err
	}

	return result, nil
}

// setupEncryption creates new key slots or adopts existing ones. Returns true
// if existing slots were adopted.
func (m *InitManager) setupEncryption(ctx context.Context, cfg initConfig) (adopted bool, err error) {
	slots, err := keychain.LoadKeySlots(m.store)
	if err != nil {
		return false, fmt.Errorf("load key slots: %w", err)
	}

	var masterKey []byte
	if len(slots) > 0 {
		// Use existing master key.
		mk, err := cfg.chain.Resolve(ctx, slots)
		if err != nil {
			return false, fmt.Errorf("found existing key slots but cannot open them: %w", err)
		}
		masterKey = mk
		adopted = true
	} else {
		// Generate new master key.
		mk, err := crypto.GenerateKey()
		if err != nil {
			return false, fmt.Errorf("generate master key for init: %w", err)
		}
		masterKey = mk
	}

	// Always wrap and write slots in the provided chain.
	// This ensures that new credentials provided during 'adopt' get their own slots.
	newSlots, err := cfg.chain.WrapAll(ctx, masterKey)
	if err != nil {
		return false, fmt.Errorf("wrap master key: %w", err)
	}
	for _, slot := range newSlots {
		if err := keychain.WriteKeySlot(m.store, slot); err != nil {
			return false, fmt.Errorf("write key slot: %w", err)
		}
	}

	return adopted, nil
}

// addRecoverySlot extracts the master key and creates a recovery slot.
func (m *InitManager) addRecoverySlot(ctx context.Context, cfg initConfig) (string, error) {
	slots, err := keychain.LoadKeySlots(m.store)
	if err != nil {
		return "", fmt.Errorf("reload key slots: %w", err)
	}
	masterKey, err := cfg.chain.Resolve(ctx, slots)
	if err != nil {
		return "", fmt.Errorf("extract master key for recovery slot: %w", err)
	}
	mnemonic, err := keychain.AddRecoverySlot(m.store, masterKey)
	if err != nil {
		return "", fmt.Errorf("create recovery key: %w", err)
	}
	return mnemonic, nil
}

func (m *InitManager) writeRepoConfig(ctx context.Context, encrypted bool) error {
	cfg := core.RepoConfig{
		Version:   1,
		Created:   time.Now().UTC().Format(time.RFC3339),
		Encrypted: encrypted,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal repo config: %w", err)
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
