package keychain

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

type mockStore struct {
	data map[string][]byte
}

func newMemStore() *mockStore {
	return &mockStore{data: make(map[string][]byte)}
}

func (m *mockStore) Put(ctx context.Context, key string, data []byte) error {
	m.data[key] = append([]byte(nil), data...)
	return nil
}

func (m *mockStore) Get(ctx context.Context, key string) ([]byte, error) {
	d, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), d...), nil
}

func (m *mockStore) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockStore) Delete(ctx context.Context, key string) error {
	delete(m.data, key)
	return nil
}

func (m *mockStore) Exists(ctx context.Context, key string) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}

func (m *mockStore) Size(ctx context.Context, key string) (int64, error) {
	d, ok := m.data[key]
	if !ok {
		return 0, nil
	}
	return int64(len(d)), nil
}

func (m *mockStore) TotalSize(ctx context.Context) (int64, error) {
	var total int64
	for _, v := range m.data {
		total += int64(len(v))
	}
	return total, nil
}

func (m *mockStore) Flush(ctx context.Context) error {
	return nil
}

// initEncryptionKey is a local test helper that replicates the old InitEncryptionKey logic
// using the new Chain/Credential API.
func initEncryptionKey(s *mockStore, platformKey []byte, password string) ([]byte, error) {
	masterKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	var c Chain
	if len(platformKey) > 0 {
		c = append(c, WithPlatformKey(platformKey))
	}
	if password != "" {
		c = append(c, WithPassword(password))
	}
	slots, err := c.WrapAll(context.Background(), masterKey)
	if err != nil {
		return nil, err
	}
	for _, slot := range slots {
		if err := WriteKeySlot(context.Background(), s, slot); err != nil {
			return nil, err
		}
	}
	return masterKey, nil
}

func TestInitAndOpenPlatformKey(t *testing.T) {
	inner := newMemStore()
	platformKey, _ := crypto.GenerateKey()

	encKey, err := initEncryptionKey(inner, platformKey, "")
	if err != nil {
		t.Fatalf("InitEncryptionKey: %v", err)
	}
	if len(encKey) != crypto.KeySize {
		t.Fatalf("encryption key length = %d, want %d", len(encKey), crypto.KeySize)
	}

	slots, err := LoadKeySlots(context.Background(), inner)
	if err != nil {
		t.Fatalf("LoadKeySlots: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(slots))
	}
	if slots[0].SlotType != "platform" {
		t.Fatalf("slot type = %q, want platform", slots[0].SlotType)
	}

	opened, err := (Chain{WithPlatformKey(platformKey)}).Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("WithPlatformKey Resolve: %v", err)
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

	encKey, err := initEncryptionKey(inner, nil, password)
	if err != nil {
		t.Fatalf("InitEncryptionKey: %v", err)
	}

	slots, err := LoadKeySlots(context.Background(), inner)
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

	opened, err := (Chain{WithPassword(password)}).Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("WithPassword Resolve: %v", err)
	}
	for i := range encKey {
		if encKey[i] != opened[i] {
			t.Fatalf("opened key differs from init key at byte %d", i)
		}
	}
}

func TestOpenWithWrongPassword(t *testing.T) {
	inner := newMemStore()
	_, err := initEncryptionKey(inner, nil, "correct-password")
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(context.Background(), inner)
	if _, err := (Chain{WithPassword("wrong-password")}).Resolve(context.Background(), slots); err == nil {
		t.Fatal("expected error with wrong password")
	}
}

func TestOpenWithWrongPlatformKey(t *testing.T) {
	inner := newMemStore()
	key1, _ := crypto.GenerateKey()
	key2, _ := crypto.GenerateKey()

	_, err := initEncryptionKey(inner, key1, "")
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(context.Background(), inner)
	if _, err := (Chain{WithPlatformKey(key2)}).Resolve(context.Background(), slots); err == nil {
		t.Fatal("expected error with wrong platform key")
	}
}

func TestDualSlots(t *testing.T) {
	inner := newMemStore()
	platformKey, _ := crypto.GenerateKey()
	password := "my-password"

	encKey, err := initEncryptionKey(inner, platformKey, password)
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(context.Background(), inner)
	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}

	opened1, err := (Chain{WithPlatformKey(platformKey)}).Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("open with platform key: %v", err)
	}
	opened2, err := (Chain{WithPassword(password)}).Resolve(context.Background(), slots)
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

	if _, err := initEncryptionKey(inner, platformKey, ""); err != nil {
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

	encKey, err := initEncryptionKey(inner, platformKey, "")
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(context.Background(), inner)
	masterKey, err := (Chain{WithPlatformKey(platformKey)}).Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("Resolve with platform key: %v", err)
	}

	mnemonic, err := AddRecoverySlot(context.Background(), inner, masterKey)
	if err != nil {
		t.Fatalf("AddRecoverySlot: %v", err)
	}
	if mnemonic == "" {
		t.Fatal("mnemonic should not be empty")
	}

	slots, _ = LoadKeySlots(context.Background(), inner)
	var hasRecovery bool
	for _, s := range slots {
		if s.SlotType == "recovery" {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatal("expected a recovery slot after AddRecoverySlot")
	}

	opened, err := (Chain{WithRecoveryKey(mnemonic)}).Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("Resolve with recovery key: %v", err)
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

	_, err := initEncryptionKey(inner, platformKey, "")
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(context.Background(), inner)
	mk, _ := (Chain{WithPlatformKey(platformKey)}).Resolve(context.Background(), slots)
	_, _ = AddRecoverySlot(context.Background(), inner, mk)

	slots, _ = LoadKeySlots(context.Background(), inner)

	// Create another valid mnemonic
	wrongMnemonic, _, _ := crypto.GenerateRecoveryMnemonic()

	if _, err := (Chain{WithRecoveryKey(wrongMnemonic)}).Resolve(context.Background(), slots); err == nil {
		t.Fatal("expected error with wrong recovery key")
	}
}

func TestChangePasswordSlot(t *testing.T) {
	inner := newMemStore()
	oldPassword := "old-password"
	newPassword := "new-password"

	encKey, err := initEncryptionKey(inner, nil, oldPassword)
	if err != nil {
		t.Fatal(err)
	}

	slots, _ := LoadKeySlots(context.Background(), inner)
	mk, err := (Chain{WithPassword(oldPassword)}).Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("Resolve with old password: %v", err)
	}

	if err := ChangePasswordSlot(context.Background(), inner, mk, newPassword); err != nil {
		t.Fatalf("ChangePasswordSlot: %v", err)
	}

	// Old password should no longer work.
	slots, _ = LoadKeySlots(context.Background(), inner)
	if _, err := (Chain{WithPassword(oldPassword)}).Resolve(context.Background(), slots); err == nil {
		t.Fatal("old password should no longer open the repo")
	}

	// New password should work and produce the same encryption key.
	opened, err := (Chain{WithPassword(newPassword)}).Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("Resolve with new password: %v", err)
	}
	for i := range encKey {
		if encKey[i] != opened[i] {
			t.Fatalf("key mismatch at byte %d after password change", i)
		}
	}
}

func TestChangePasswordSlot_EmptyPassword(t *testing.T) {
	inner := newMemStore()
	mk, _ := crypto.GenerateKey()
	if err := ChangePasswordSlot(context.Background(), inner, mk, ""); err == nil {
		t.Fatal("expected error for empty password")
	}
}

func TestHasKeySlots(t *testing.T) {
	inner := newMemStore()
	if HasKeySlots(context.Background(), inner) {
		t.Fatal("empty store should not have key slots")
	}
	key, _ := crypto.GenerateKey()
	_, _ = initEncryptionKey(inner, key, "")
	if !HasKeySlots(context.Background(), inner) {
		t.Fatal("store should have key slots after init")
	}
}
