package keychain

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

type mockKMSClient struct {
	key []byte
}

func (m *mockKMSClient) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	return crypto.WrapKey(plaintext, m.key)
}

func (m *mockKMSClient) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	return crypto.UnwrapKey(ciphertext, m.key)
}

func TestKMSClientCred(t *testing.T) {
	ctx := context.Background()
	masterKey, _ := crypto.GenerateKey()
	kmsKey, _ := crypto.GenerateKey()
	mock := &mockKMSClient{key: kmsKey}

	cred := WithKMSClient(mock)

	// Wrap
	slot, err := cred.Wrap(ctx, masterKey)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if slot.SlotType != "kms-platform" {
		t.Errorf("slot type = %q, want kms-platform", slot.SlotType)
	}

	// Resolve
	resolved, err := cred.Resolve(ctx, []KeySlot{slot})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(resolved) != string(masterKey) {
		t.Error("resolved key mismatch")
	}

	// Resolve with wrong slot type should fail
	wrongSlot := slot
	wrongSlot.SlotType = "password"
	if _, err := cred.Resolve(ctx, []KeySlot{wrongSlot}); err == nil {
		t.Error("expected error for wrong slot type")
	}

	// Resolve with nil client should fail
	if _, err := WithKMSClient(nil).Resolve(ctx, []KeySlot{slot}); err == nil {
		t.Error("expected error for nil client")
	}
}

func TestChainResolve_PrioritizeResolvers(t *testing.T) {
	ctx := context.Background()
	masterKey, _ := crypto.GenerateKey()

	pass1 := "pass1"
	pass2 := "pass2"

	slot1, _ := CreatePasswordSlot(masterKey, pass1)
	slot2, _ := CreatePasswordSlot(masterKey, pass2)

	slots := []KeySlot{slot1, slot2}

	// Chain with pass2 should work even if slot1 is first in list
	c := Chain{WithPassword(pass2)}
	resolved, err := c.Resolve(ctx, slots)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(resolved) != string(masterKey) {
		t.Error("resolved key mismatch")
	}
}

func TestDeriveEncryptionKey(t *testing.T) {
	masterKey := []byte("master-key-32-bytes-long-1234567")
	key, err := DeriveEncryptionKey(masterKey)
	if err != nil {
		t.Fatalf("DeriveEncryptionKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("expected 32 bytes, got %d", len(key))
	}

	// Test determinism
	key2, _ := DeriveEncryptionKey(masterKey)
	if string(key) != string(key2) {
		t.Error("DeriveEncryptionKey not deterministic")
	}
}

func TestChainWrapAll_ErrCannotWrap(t *testing.T) {
	ctx := context.Background()
	masterKey, _ := crypto.GenerateKey()

	// recoveryCred returns ErrCannotWrap
	c := Chain{WithRecoveryKey("mnemonic"), WithPassword("pass")}
	slots, err := c.WrapAll(ctx, masterKey)
	if err != nil {
		t.Fatalf("WrapAll: %v", err)
	}

	// Should only have 1 slot (password)
	if len(slots) != 1 {
		t.Errorf("expected 1 slot, got %d", len(slots))
	}
	if slots[0].SlotType != "password" {
		t.Errorf("expected password slot, got %s", slots[0].SlotType)
	}
}

func TestResolve_EmptyConfig(t *testing.T) {
	ctx := context.Background()
	c := Chain{WithPassword("pass")}

	// No slots
	_, err := c.Resolve(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "no key slots found") {
		t.Errorf("expected 'no key slots found' error, got %v", err)
	}

	// Empty chain
	slots := []KeySlot{{SlotType: "password"}}
	_, err = Chain{}.Resolve(ctx, slots)
	if err == nil || !strings.Contains(err.Error(), "no resolvers configured in the keychain") {
		t.Errorf("expected 'no resolvers' error, got %v", err)
	}
}

func TestWithPrompt(t *testing.T) {
	ctx := context.Background()
	masterKey, _ := crypto.GenerateKey()
	password := "prompt-pass"

	slot, _ := CreatePasswordSlot(masterKey, password)
	slots := []KeySlot{slot}

	// Test successful prompt
	cred := WithPrompt(func() (string, error) {
		return password, nil
	}, func() (string, error) {
		return password, nil
	})
	resolved, err := cred.Resolve(ctx, slots)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(resolved) != string(masterKey) {
		t.Error("resolved key mismatch")
	}

	// Test prompt error
	errCred := WithPrompt(func() (string, error) {
		return "", fmt.Errorf("prompt error")
	}, nil)
	if _, err := errCred.Resolve(ctx, slots); err == nil {
		t.Error("expected error for prompt error")
	}

	// Test no password slot (should not prompt if we check slots first)
	noPassSlots := []KeySlot{{SlotType: "platform"}}
	prompted := false
	lazyCred := WithPrompt(func() (string, error) {
		prompted = true
		return password, nil
	}, nil)
	if _, err := lazyCred.Resolve(ctx, noPassSlots); err == nil {
		t.Error("expected error for missing password slot")
	}
	if prompted {
		t.Error("should not have prompted user if no password slot exists")
	}
}

func TestCreatePlatformSlot(t *testing.T) {
	masterKey, _ := crypto.GenerateKey()
	platformKey, _ := crypto.GenerateKey()

	slot, err := CreatePlatformSlot(masterKey, platformKey)
	if err != nil {
		t.Fatalf("CreatePlatformSlot: %v", err)
	}
	if slot.SlotType != "platform" {
		t.Errorf("expected platform slot type, got %s", slot.SlotType)
	}

	// Round trip check
	resolved, err := unwrapMasterKey(slot, platformKey)
	if err != nil {
		t.Fatalf("unwrapMasterKey: %v", err)
	}
	if string(resolved) != string(masterKey) {
		t.Error("resolved key mismatch")
	}
}
