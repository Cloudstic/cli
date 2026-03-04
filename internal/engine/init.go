package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

// InitOption configures an init operation.
type InitOption func(*initConfig)

type initConfig struct {
	platformKey  []byte
	password     string
	recovery     bool
	noEncryption bool
	kmsEncrypter crypto.KMSEncrypter
	kmsDecrypter crypto.KMSDecrypter
	kmsKeyARN    string
}

// WithInitPlatformKey sets the platform key for encryption.
func WithInitPlatformKey(key []byte) InitOption {
	return func(cfg *initConfig) { cfg.platformKey = key }
}

// WithInitPassword sets the password for encryption.
func WithInitPassword(pw string) InitOption {
	return func(cfg *initConfig) { cfg.password = pw }
}

// WithInitRecovery requests generation of a recovery key during init.
func WithInitRecovery() InitOption {
	return func(cfg *initConfig) { cfg.recovery = true }
}

// WithInitNoEncryption creates an unencrypted repository.
func WithInitNoEncryption() InitOption {
	return func(cfg *initConfig) { cfg.noEncryption = true }
}

// WithInitKMS configures KMS envelope encryption for the repository.
// The encrypter wraps the master key during init; the decrypter is used
// to verify existing KMS slots and to extract the master key when adding
// a recovery slot.
func WithInitKMS(encrypter crypto.KMSEncrypter, decrypter crypto.KMSDecrypter, keyARN string) InitOption {
	return func(cfg *initConfig) {
		cfg.kmsEncrypter = encrypter
		cfg.kmsDecrypter = decrypter
		cfg.kmsKeyARN = keyARN
	}
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

	// Check if already initialized.
	cfgData, err := m.store.Get(ctx, configKey)
	if err == nil && cfgData != nil {
		return nil, fmt.Errorf("repository is already initialized")
	}

	hasCreds := len(cfg.platformKey) > 0 || cfg.password != "" || cfg.kmsEncrypter != nil
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
	slots, err := store.LoadKeySlots(m.store)
	if err != nil {
		return false, fmt.Errorf("load key slots: %w", err)
	}

	if len(slots) > 0 {
		// Verify we can open the existing slots.
		if err := verifyExistingSlots(ctx, slots, cfg); err != nil {
			return false, fmt.Errorf("found existing key slots but cannot open them: %w", err)
		}
		return true, nil
	}

	if cfg.kmsEncrypter != nil {
		if _, err := store.InitKMSEncryptionKey(ctx, m.store, cfg.kmsEncrypter, cfg.kmsKeyARN, cfg.platformKey, cfg.password); err != nil {
			return false, fmt.Errorf("initialize KMS encryption: %w", err)
		}
	} else {
		if _, err := store.InitEncryptionKey(m.store, cfg.platformKey, cfg.password); err != nil {
			return false, fmt.Errorf("initialize encryption: %w", err)
		}
	}
	return false, nil
}

// verifyExistingSlots checks that at least one provided credential can open
// the existing key slots.
func verifyExistingSlots(ctx context.Context, slots []store.KeySlot, cfg initConfig) error {
	if cfg.kmsDecrypter != nil {
		if _, err := store.OpenWithKMS(ctx, slots, cfg.kmsDecrypter); err == nil {
			return nil
		}
	}
	if len(cfg.platformKey) > 0 {
		if _, err := store.OpenWithPlatformKey(slots, cfg.platformKey); err == nil {
			return nil
		}
	}
	if cfg.password != "" {
		if _, err := store.OpenWithPassword(slots, cfg.password); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no provided credential matches (types: %s)", store.SlotTypes(slots))
}

// addRecoverySlot extracts the master key and creates a recovery slot.
func (m *InitManager) addRecoverySlot(ctx context.Context, cfg initConfig) (string, error) {
	slots, err := store.LoadKeySlots(m.store)
	if err != nil {
		return "", fmt.Errorf("reload key slots: %w", err)
	}
	masterKey, err := store.ExtractMasterKeyWithKMS(ctx, slots, cfg.kmsDecrypter, cfg.platformKey, cfg.password)
	if err != nil {
		return "", fmt.Errorf("extract master key for recovery slot: %w", err)
	}
	mnemonic, err := store.AddRecoverySlot(m.store, masterKey)
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
