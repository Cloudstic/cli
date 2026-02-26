package store

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

func TestInitAndOpenPlatformKey(t *testing.T) {
	inner := newMemStore()
	platformKey, _ := crypto.GenerateKey()

	encKey, err := InitEncryptionKey(inner, platformKey, "")
	if err != nil {
		t.Fatalf("InitEncryptionKey: %v", err)
	}
	if len(encKey) != crypto.KeySize {
		t.Fatalf("encryption key length = %d, want %d", len(encKey), crypto.KeySize)
	}

	slots, err := LoadKeySlots(inner)
	if err != nil {
		t.Fatalf("LoadKeySlots: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(slots))
	}
	if slots[0].SlotType != "platform" {
		t.Fatalf("slot type = %q, want platform", slots[0].SlotType)
	}

	opened, err := OpenWithPlatformKey(slots, platformKey)
	if err != nil {
		t.Fatalf("OpenWithPlatformKey: %v", err)
	}
	if len(opened) != crypto.KeySize {
		t.Fatalf("opened key length = %d, want %d", len(opened), crypto.KeySize)
	}
	for i := range encKey {
		if encKey[i] != opened[i] {
			t.Fatalf("opened key differs from init key at byte %d", i)
		}
	}
}

func TestInitAndOpenPassword(t *testing.T) {
	inner := newMemStore()
	password := "test-password-123"

	encKey, err := InitEncryptionKey(inner, nil, password)
	if err != nil {
		t.Fatalf("InitEncryptionKey: %v", err)
	}

	slots, err := LoadKeySlots(inner)
	if err != nil {
		t.Fatalf("LoadKeySlots: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(slots))
	}
	if slots[0].SlotType != "password" {
		t.Fatalf("slot type = %q, want password", slots[0].SlotType)
	}
	if slots[0].KDFParams == nil {
		t.Fatal("password slot missing KDFParams")
	}
	if slots[0].KDFParams.Algorithm != "argon2id" {
		t.Fatalf("algorithm = %q, want argon2id", slots[0].KDFParams.Algorithm)
	}

	opened, err := OpenWithPassword(slots, password)
	if err != nil {
		t.Fatalf("OpenWithPassword: %v", err)
	}
	for i := range encKey {
		if encKey[i] != opened[i] {
			t.Fatalf("opened key differs from init key at byte %d", i)
		}
	}
}

func TestOpenWithWrongPassword(t *testing.T) {
	inner := newMemStore()
	_, err := InitEncryptionKey(inner, nil, "correct-password")
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(inner)
	if _, err := OpenWithPassword(slots, "wrong-password"); err == nil {
		t.Fatal("expected error with wrong password")
	}
}

func TestOpenWithWrongPlatformKey(t *testing.T) {
	inner := newMemStore()
	key1, _ := crypto.GenerateKey()
	key2, _ := crypto.GenerateKey()

	_, err := InitEncryptionKey(inner, key1, "")
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(inner)
	if _, err := OpenWithPlatformKey(slots, key2); err == nil {
		t.Fatal("expected error with wrong platform key")
	}
}

func TestDualSlots(t *testing.T) {
	inner := newMemStore()
	platformKey, _ := crypto.GenerateKey()
	password := "my-password"

	encKey, err := InitEncryptionKey(inner, platformKey, password)
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(inner)
	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}

	opened1, err := OpenWithPlatformKey(slots, platformKey)
	if err != nil {
		t.Fatalf("open with platform key: %v", err)
	}
	opened2, err := OpenWithPassword(slots, password)
	if err != nil {
		t.Fatalf("open with password: %v", err)
	}

	for i := range encKey {
		if encKey[i] != opened1[i] || encKey[i] != opened2[i] {
			t.Fatalf("all three keys must match at byte %d", i)
		}
	}
}

func TestKeySlotJSONFormat(t *testing.T) {
	inner := newMemStore()
	platformKey, _ := crypto.GenerateKey()

	if _, err := InitEncryptionKey(inner, platformKey, ""); err != nil {
		t.Fatal(err)
	}

	raw := inner.data["keys/platform-default"]
	var slot KeySlot
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if slot.SlotType != "platform" {
		t.Fatalf("slot_type = %q, want platform", slot.SlotType)
	}
	if slot.Label != "default" {
		t.Fatalf("label = %q, want default", slot.Label)
	}
	if _, err := base64.StdEncoding.DecodeString(slot.WrappedKey); err != nil {
		t.Fatalf("wrapped_key not valid base64: %v", err)
	}
}

func TestAddAndOpenRecoveryKey(t *testing.T) {
	inner := newMemStore()
	platformKey, _ := crypto.GenerateKey()

	encKey, err := InitEncryptionKey(inner, platformKey, "")
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(inner)
	masterKey, err := ExtractMasterKey(slots, platformKey, "")
	if err != nil {
		t.Fatalf("ExtractMasterKey: %v", err)
	}

	mnemonic, err := AddRecoverySlot(inner, masterKey)
	if err != nil {
		t.Fatalf("AddRecoverySlot: %v", err)
	}
	if mnemonic == "" {
		t.Fatal("mnemonic should not be empty")
	}

	slots, _ = LoadKeySlots(inner)
	var hasRecovery bool
	for _, s := range slots {
		if s.SlotType == "recovery" {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatal("expected a recovery slot after AddRecoverySlot")
	}

	recoveryKey, err := crypto.MnemonicToKey(mnemonic)
	if err != nil {
		t.Fatalf("MnemonicToKey: %v", err)
	}
	opened, err := OpenWithRecoveryKey(slots, recoveryKey)
	if err != nil {
		t.Fatalf("OpenWithRecoveryKey: %v", err)
	}
	for i := range encKey {
		if encKey[i] != opened[i] {
			t.Fatalf("recovery-opened key differs at byte %d", i)
		}
	}
}

func TestOpenWithWrongRecoveryKey(t *testing.T) {
	inner := newMemStore()
	platformKey, _ := crypto.GenerateKey()

	_, err := InitEncryptionKey(inner, platformKey, "")
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(inner)
	mk, _ := ExtractMasterKey(slots, platformKey, "")
	_, _ = AddRecoverySlot(inner, mk)

	slots, _ = LoadKeySlots(inner)
	wrongKey, _ := crypto.GenerateKey()
	if _, err := OpenWithRecoveryKey(slots, wrongKey); err == nil {
		t.Fatal("expected error with wrong recovery key")
	}
}

func TestExtractMasterKey_Platform(t *testing.T) {
	inner := newMemStore()
	platformKey, _ := crypto.GenerateKey()
	InitEncryptionKey(inner, platformKey, "")

	slots, _ := LoadKeySlots(inner)
	mk, err := ExtractMasterKey(slots, platformKey, "")
	if err != nil {
		t.Fatalf("ExtractMasterKey: %v", err)
	}
	if len(mk) != crypto.KeySize {
		t.Fatalf("master key length = %d, want %d", len(mk), crypto.KeySize)
	}
}

func TestExtractMasterKey_Password(t *testing.T) {
	inner := newMemStore()
	password := "test-pass"
	InitEncryptionKey(inner, nil, password)

	slots, _ := LoadKeySlots(inner)
	mk, err := ExtractMasterKey(slots, nil, password)
	if err != nil {
		t.Fatalf("ExtractMasterKey: %v", err)
	}
	if len(mk) != crypto.KeySize {
		t.Fatalf("master key length = %d, want %d", len(mk), crypto.KeySize)
	}
}

func TestExtractMasterKey_NoMatch(t *testing.T) {
	inner := newMemStore()
	key, _ := crypto.GenerateKey()
	InitEncryptionKey(inner, key, "")

	slots, _ := LoadKeySlots(inner)
	wrongKey, _ := crypto.GenerateKey()
	if _, err := ExtractMasterKey(slots, wrongKey, ""); err == nil {
		t.Fatal("expected error with wrong credentials")
	}
}

func TestHasKeySlots(t *testing.T) {
	inner := newMemStore()
	if HasKeySlots(inner) {
		t.Fatal("empty store should not have key slots")
	}
	key, _ := crypto.GenerateKey()
	InitEncryptionKey(inner, key, "")
	if !HasKeySlots(inner) {
		t.Fatal("store should have key slots after init")
	}
}
