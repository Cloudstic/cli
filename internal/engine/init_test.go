package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/keychain"
)

// mockKMS implements both crypto.KMSEncrypter and crypto.KMSDecrypter using
// simple XOR "encryption" so tests don't need real AWS credentials.
type mockKMS struct {
	// xorByte is used to reversibly transform plaintext ↔ ciphertext.
	xorByte byte
}

func (m *mockKMS) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[i] = b ^ m.xorByte
	}
	return out, nil
}

func (m *mockKMS) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	out := make([]byte, len(ciphertext))
	for i, b := range ciphertext {
		out[i] = b ^ m.xorByte
	}
	return out, nil
}

var _ crypto.KMSClient = (*mockKMS)(nil)

func TestInitManager_UnencryptedRepo(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)

	result, err := mgr.Run(context.Background(), WithInitNoEncryption())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Encrypted {
		t.Error("expected unencrypted repo")
	}

	// Verify config was written.
	data, err := s.Get(context.Background(), "config")
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var cfg core.RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid config JSON: %v", err)
	}
	if cfg.Encrypted {
		t.Error("config should not be encrypted")
	}
	if cfg.Version != 1 {
		t.Errorf("expected version 1, got %d", cfg.Version)
	}
}

func TestInitManager_EncryptedWithPassword(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)

	result, err := mgr.Run(context.Background(), WithInitCredentials(keychain.Chain{keychain.WithPassword("test-password")}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Encrypted {
		t.Error("expected encrypted repo")
	}
	if result.AdoptedSlots {
		t.Error("expected new slots, not adopted")
	}

	// Verify key slots were created.
	slots, err := keychain.LoadKeySlots(s)
	if err != nil {
		t.Fatalf("failed to load key slots: %v", err)
	}
	if len(slots) == 0 {
		t.Fatal("expected at least one key slot")
	}
	found := false
	for _, slot := range slots {
		if slot.SlotType == "password" {
			found = true
		}
	}
	if !found {
		t.Error("expected a password key slot")
	}

	// Verify we can open the slot.
	if _, err := (keychain.Chain{keychain.WithPassword("test-password")}).Resolve(context.Background(), slots); err != nil {
		t.Errorf("failed to open with password: %v", err)
	}
}

func TestInitManager_EncryptedWithPlatformKey(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)

	platformKey := make([]byte, 32)
	for i := range platformKey {
		platformKey[i] = byte(i)
	}

	result, err := mgr.Run(context.Background(), WithInitCredentials(keychain.Chain{keychain.WithPlatformKey(platformKey)}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Encrypted {
		t.Error("expected encrypted repo")
	}

	slots, err := keychain.LoadKeySlots(s)
	if err != nil {
		t.Fatalf("failed to load key slots: %v", err)
	}
	if _, err := (keychain.Chain{keychain.WithPlatformKey(platformKey)}).Resolve(context.Background(), slots); err != nil {
		t.Errorf("failed to open with platform key: %v", err)
	}
}

func TestInitManager_AlreadyInitialized(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)

	// First init.
	if _, err := mgr.Run(context.Background(), WithInitNoEncryption()); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	// Second init should fail.
	_, err := mgr.Run(context.Background(), WithInitNoEncryption())
	if err == nil {
		t.Fatal("expected error for already-initialized repo")
	}
}

func TestInitManager_AdoptsExistingSlots(t *testing.T) {
	s := NewMockStore()

	// Pre-create key slots (simulate a partially initialized repo without config).
	platformKey := make([]byte, 32)
	for i := range platformKey {
		platformKey[i] = byte(i + 10)
	}
	masterKey, _ := crypto.GenerateKey()
	slot, _ := keychain.CreatePlatformSlot(masterKey, platformKey)
	_ = keychain.WriteKeySlot(s, slot)

	mgr := NewInitManager(s)
	result, err := mgr.Run(context.Background(), WithInitCredentials(keychain.Chain{keychain.WithPlatformKey(platformKey)}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.AdoptedSlots {
		t.Error("expected AdoptedSlots to be true")
	}
}

func TestInitManager_AdoptExistingSlots_WrongCredential(t *testing.T) {
	s := NewMockStore()

	// Pre-create key slots with one platform key.
	originalKey := make([]byte, 32)
	for i := range originalKey {
		originalKey[i] = byte(i)
	}
	masterKey, _ := crypto.GenerateKey()
	slot, _ := keychain.CreatePlatformSlot(masterKey, originalKey)
	_ = keychain.WriteKeySlot(s, slot)

	// Try to init with a different platform key.
	wrongKey := make([]byte, 32)
	for i := range wrongKey {
		wrongKey[i] = byte(i + 100)
	}

	mgr := NewInitManager(s)
	_, err := mgr.Run(context.Background(), WithInitCredentials(keychain.Chain{keychain.WithPlatformKey(wrongKey)}))
	if err == nil {
		t.Fatal("expected error when adopting slots with wrong key")
	}
}

func TestInitManager_WithRecoveryKey(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)

	result, err := mgr.Run(context.Background(),
		WithInitCredentials(keychain.Chain{keychain.WithPassword("test-password")}),
		WithInitRecovery(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RecoveryKey == "" {
		t.Error("expected recovery key mnemonic")
	}

	// Should be a 24-word mnemonic.
	words := 0
	for _, c := range result.RecoveryKey {
		if c == ' ' {
			words++
		}
	}
	words++ // last word has no trailing space
	if words != 24 {
		t.Errorf("expected 24-word mnemonic, got %d words", words)
	}

	// Verify recovery slot was created.
	slots, err := keychain.LoadKeySlots(s)
	if err != nil {
		t.Fatalf("failed to load key slots: %v", err)
	}
	found := false
	for _, slot := range slots {
		if slot.SlotType == "recovery" {
			found = true
		}
	}
	if !found {
		t.Error("expected a recovery key slot")
	}
}

func TestInitManager_EncryptedWithKMS(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)
	kms := &mockKMS{xorByte: 0x42}

	result, err := mgr.Run(context.Background(),
		WithInitCredentials(keychain.Chain{keychain.WithKMSClient(kms)}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Encrypted {
		t.Error("expected encrypted repo")
	}
	if result.AdoptedSlots {
		t.Error("expected new slots, not adopted")
	}

	// Verify kms-platform slot was created.
	slots, err := keychain.LoadKeySlots(s)
	if err != nil {
		t.Fatalf("failed to load key slots: %v", err)
	}
	found := false
	for _, slot := range slots {
		if slot.SlotType == "kms-platform" {
			found = true
		}
	}
	if !found {
		t.Error("expected a kms-platform key slot")
	}

	// Verify we can open the slot with the mock decrypter.
	if _, err := (keychain.Chain{keychain.WithKMSClient(kms)}).Resolve(context.Background(), slots); err != nil {
		t.Errorf("failed to open with KMS: %v", err)
	}

	// Verify config was written.
	data, err := s.Get(context.Background(), "config")
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var cfg core.RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid config JSON: %v", err)
	}
	if !cfg.Encrypted {
		t.Error("config should be encrypted")
	}
}

func TestInitManager_KMSWithPasswordSlots(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)
	kms := &mockKMS{xorByte: 0x42}

	// Init with both KMS and password — should create both slot types.
	result, err := mgr.Run(context.Background(),
		WithInitCredentials(keychain.Chain{
			keychain.WithKMSClient(kms),
			keychain.WithPassword("test-password"),
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Encrypted {
		t.Error("expected encrypted repo")
	}

	slots, err := keychain.LoadKeySlots(s)
	if err != nil {
		t.Fatalf("failed to load key slots: %v", err)
	}

	hasKMS, hasPW := false, false
	for _, slot := range slots {
		switch slot.SlotType {
		case "kms-platform":
			hasKMS = true
		case "password":
			hasPW = true
		}
	}
	if !hasKMS {
		t.Error("expected a kms-platform key slot")
	}
	if !hasPW {
		t.Error("expected a password key slot")
	}

	// Both credential types should open the repo.
	if _, err := (keychain.Chain{keychain.WithKMSClient(kms)}).Resolve(context.Background(), slots); err != nil {
		t.Errorf("failed to open with KMS: %v", err)
	}
	if _, err := (keychain.Chain{keychain.WithPassword("test-password")}).Resolve(context.Background(), slots); err != nil {
		t.Errorf("failed to open with password: %v", err)
	}
}

func TestInitManager_KMSWithRecovery(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)
	kms := &mockKMS{xorByte: 0x42}

	result, err := mgr.Run(context.Background(),
		WithInitCredentials(keychain.Chain{keychain.WithKMSClient(kms)}),
		WithInitRecovery(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RecoveryKey == "" {
		t.Error("expected recovery key mnemonic")
	}

	slots, err := keychain.LoadKeySlots(s)
	if err != nil {
		t.Fatalf("failed to load key slots: %v", err)
	}
	hasRecovery := false
	for _, slot := range slots {
		if slot.SlotType == "recovery" {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Error("expected a recovery key slot")
	}
}

func TestInitManager_NoEncryptionOverridesCreds(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)

	// Passing both a password and --no-encryption should result in unencrypted repo.
	result, err := mgr.Run(context.Background(),
		WithInitCredentials(keychain.Chain{keychain.WithPassword("test-password")}),
		WithInitNoEncryption(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Encrypted {
		t.Error("expected unencrypted repo when --no-encryption is set")
	}

	// No key slots should exist.
	slots, err := keychain.LoadKeySlots(s)
	if err != nil {
		t.Fatalf("failed to load key slots: %v", err)
	}
	if len(slots) != 0 {
		t.Errorf("expected no key slots, got %d", len(slots))
	}
}
func TestInitManager_AdoptAddsNewSlots(t *testing.T) {
	s := NewMockStore()
	mgr := NewInitManager(s)
	ctx := context.Background()

	// 1. Pre-create a password slot
	password := "p1"
	masterKey, _ := crypto.GenerateKey()
	slot, _ := keychain.CreatePasswordSlot(masterKey, password)
	_ = keychain.WriteKeySlot(s, slot)

	// 2. Adopt with password AND a new platform key
	platformKey := make([]byte, 32)
	for i := range platformKey {
		platformKey[i] = byte(i + 5)
	}

	result, err := mgr.Run(ctx,
		WithInitCredentials(keychain.Chain{
			keychain.WithPassword(password),
			keychain.WithPlatformKey(platformKey),
		}),
		WithInitAdoptSlots(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.AdoptedSlots {
		t.Error("expected AdoptedSlots to be true")
	}

	// 3. Verify both slots exist and work
	slots, err := keychain.LoadKeySlots(s)
	if err != nil {
		t.Fatalf("failed to load key slots: %v", err)
	}
	if len(slots) < 2 {
		t.Errorf("expected at least 2 slots, got %d", len(slots))
	}

	hasPW, hasPlatform := false, false
	for _, sl := range slots {
		if sl.SlotType == "password" {
			hasPW = true
		}
		if sl.SlotType == "platform" {
			hasPlatform = true
		}
	}
	if !hasPW || !hasPlatform {
		t.Errorf("missing expected slot types: hasPW=%v, hasPlatform=%v", hasPW, hasPlatform)
	}

	// Verify both can unlock
	if _, err := (keychain.Chain{keychain.WithPassword(password)}).Resolve(ctx, slots); err != nil {
		t.Errorf("failed to open with password: %v", err)
	}
	if _, err := (keychain.Chain{keychain.WithPlatformKey(platformKey)}).Resolve(ctx, slots); err != nil {
		t.Errorf("failed to open with platform key: %v", err)
	}
}
