package cloudstic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/keychain"
)

// mockStore is a simple in-memory store for testing
type mockStore struct {
	data map[string][]byte
}

func newMockStore() *mockStore {
	return &mockStore{
		data: make(map[string][]byte),
	}
}

func (s *mockStore) Put(_ context.Context, key string, data []byte) error {
	s.data[key] = data
	return nil
}

func (s *mockStore) Get(_ context.Context, key string) ([]byte, error) {
	data, ok := s.data[key]
	if !ok {
		return nil, nil
	}
	return data, nil
}

func (s *mockStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.data[key]
	return ok, nil
}

func (s *mockStore) Delete(_ context.Context, key string) error {
	delete(s.data, key)
	return nil
}

func (s *mockStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range s.data {
		if len(prefix) == 0 || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (s *mockStore) Size(_ context.Context, key string) (int64, error) {
	data, ok := s.data[key]
	if !ok {
		return 0, nil
	}
	return int64(len(data)), nil
}

func (s *mockStore) TotalSize(_ context.Context) (int64, error) {
	var total int64
	for _, d := range s.data {
		total += int64(len(d))
	}
	return total, nil
}

func (s *mockStore) Flush(_ context.Context) error {
	return nil
}

// ---------------------------------------------------------------------------
// LoadRepoConfig
// ---------------------------------------------------------------------------

func TestLoadRepoConfig_Uninitialized(t *testing.T) {
	ctx := context.Background()
	cfg, err := LoadRepoConfig(ctx, newMockStore())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for uninitialized repo")
	}
}

func TestLoadRepoConfig_Unencrypted(t *testing.T) {
	ctx := context.Background()
	s := newMockStore()
	if _, err := InitRepo(ctx, s, WithInitNoEncryption()); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	cfg, err := LoadRepoConfig(ctx, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config, got nil")
	} else if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	} else if cfg.Encrypted {
		t.Error("expected unencrypted config")
	}
}

func TestLoadRepoConfig_Encrypted(t *testing.T) {
	ctx := context.Background()
	s := newMockStore()
	if _, err := InitRepo(ctx, s, WithInitCredentials(keychain.Chain{keychain.WithPassword("test-pass")})); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	cfg, err := LoadRepoConfig(ctx, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config, got nil")
	} else if !cfg.Encrypted {
		t.Error("expected encrypted config")
	}
}

func TestLoadRepoConfig_Malformed(t *testing.T) {
	ctx := context.Background()
	s := newMockStore()
	_ = s.Put(ctx, "config", []byte("not json"))
	_, err := LoadRepoConfig(ctx, s)
	if err == nil {
		t.Error("expected error for malformed config")
	}
}

// ---------------------------------------------------------------------------
// ChangePassword
// ---------------------------------------------------------------------------

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	s := newMockStore()
	if _, err := InitRepo(ctx, s, WithInitCredentials(keychain.Chain{keychain.WithPassword("old-pass")})); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	if err := ChangePassword(ctx, s, keychain.Chain{keychain.WithPassword("old-pass")}, PasswordString("new-pass")); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	slots, _ := keychain.LoadKeySlots(ctx, s)
	if _, err := (keychain.Chain{keychain.WithPassword("old-pass")}).Resolve(ctx, slots); err == nil {
		t.Error("old password should no longer work")
	}
	if _, err := (keychain.Chain{keychain.WithPassword("new-pass")}).Resolve(ctx, slots); err != nil {
		t.Errorf("new password should work: %v", err)
	}
}

func TestChangePassword_WrongCredentials(t *testing.T) {
	ctx := context.Background()
	s := newMockStore()
	if _, err := InitRepo(ctx, s, WithInitCredentials(keychain.Chain{keychain.WithPassword("correct-pass")})); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if err := ChangePassword(ctx, s, keychain.Chain{keychain.WithPassword("wrong-pass")}, PasswordString("new-pass")); err == nil {
		t.Error("expected error with wrong credentials")
	}
}

// ---------------------------------------------------------------------------
// AddRecoveryKey
// ---------------------------------------------------------------------------

func TestAddRecoveryKey(t *testing.T) {
	ctx := context.Background()
	s := newMockStore()
	if _, err := InitRepo(ctx, s, WithInitCredentials(keychain.Chain{keychain.WithPassword("test-pass")})); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	mnemonic, err := AddRecoveryKey(ctx, s, keychain.Chain{keychain.WithPassword("test-pass")})
	if err != nil {
		t.Fatalf("AddRecoveryKey: %v", err)
	}
	if mnemonic == "" {
		t.Error("expected non-empty mnemonic")
	}

	slots, _ := keychain.LoadKeySlots(ctx, s)
	hasRecovery := false
	for _, slot := range slots {
		if slot.SlotType == "recovery" {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Error("expected a recovery slot after AddRecoveryKey")
	}

	// Verify the mnemonic can actually open the repo.
	if _, err := (keychain.Chain{keychain.WithRecoveryKey(mnemonic)}).Resolve(ctx, slots); err != nil {
		t.Errorf("Resolve recovery key: %v", err)
	}
}

func TestAddRecoveryKey_WrongCredentials(t *testing.T) {
	ctx := context.Background()
	s := newMockStore()
	if _, err := InitRepo(ctx, s, WithInitCredentials(keychain.Chain{keychain.WithPassword("correct-pass")})); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if _, err := AddRecoveryKey(ctx, s, keychain.Chain{keychain.WithPassword("wrong-pass")}); err == nil {
		t.Error("expected error with wrong credentials")
	}
}

func TestClientDiscoverSources(t *testing.T) {
	c := &Client{}
	if _, err := c.DiscoverSources(context.Background()); err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
}

func TestClientPlanWorkstationSetup(t *testing.T) {
	c := &Client{}
	if _, err := c.PlanWorkstationSetup(context.Background()); err != nil {
		t.Fatalf("PlanWorkstationSetup: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cat
// ---------------------------------------------------------------------------

func TestClient_Cat_SingleObject(t *testing.T) {
	ctx := context.Background()
	base := newMockStore()

	// Add a test config object
	config := core.RepoConfig{
		Version:   1,
		Created:   "2024-01-01T00:00:00Z",
		Encrypted: false,
	}
	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}
	if err := base.Put(ctx, "config", configData); err != nil {
		t.Fatalf("Failed to put config: %v", err)
	}

	client, err := NewClient(context.Background(), base)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	results, err := client.Cat(ctx, "config")
	if err != nil {
		t.Fatalf("Cat failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Key != "config" {
		t.Errorf("Expected key 'config', got %q", results[0].Key)
	}

	var gotConfig core.RepoConfig
	if err := json.Unmarshal(results[0].Data, &gotConfig); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	if gotConfig.Version != config.Version {
		t.Errorf("Expected version %d, got %d", config.Version, gotConfig.Version)
	}
	if gotConfig.Encrypted != config.Encrypted {
		t.Errorf("Expected encrypted %v, got %v", config.Encrypted, gotConfig.Encrypted)
	}
}

func TestClient_Cat_MultipleObjects(t *testing.T) {
	ctx := context.Background()
	base := newMockStore()

	// Add test objects
	testData := map[string]string{
		"config":       `{"version":1,"created":"2024-01-01T00:00:00Z","encrypted":false}`,
		"index/latest": `{"latest_snapshot":"snapshot/abc123","seq":1}`,
		"snapshot/abc": `{"version":1,"created":"2024-01-01T00:00:00Z","root":"node/def456","seq":1}`,
	}

	for key, data := range testData {
		if err := base.Put(ctx, key, []byte(data)); err != nil {
			t.Fatalf("Failed to put %s: %v", key, err)
		}
	}

	client, err := NewClient(context.Background(), base)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	results, err := client.Cat(ctx, "config", "index/latest", "snapshot/abc")
	if err != nil {
		t.Fatalf("Cat failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}

	// Verify each result
	for i, key := range []string{"config", "index/latest", "snapshot/abc"} {
		if results[i].Key != key {
			t.Errorf("Result %d: expected key %q, got %q", i, key, results[i].Key)
		}

		if string(results[i].Data) != testData[key] {
			t.Errorf("Result %d: data mismatch for key %q", i, key)
		}
	}
}

func TestClient_Cat_ObjectNotFound(t *testing.T) {
	ctx := context.Background()
	base := newMockStore()
	config := []byte(`{"version":1,"created":"2024-01-01T00:00:00Z","encrypted":false}`)
	if err := base.Put(ctx, "config", config); err != nil {
		t.Fatalf("Failed to setup mock data: %v", err)
	}

	client, err := NewClient(context.Background(), base)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	_, err = client.Cat(ctx, "nonexistent")
	if err == nil {
		t.Fatal("Expected error for nonexistent object, got nil")
	}

	expectedMsg := "object not found: \"nonexistent\""
	if err.Error() != expectedMsg {
		t.Errorf("Expected error %q, got %q", expectedMsg, err.Error())
	}
}

func TestClient_Cat_NoKeys(t *testing.T) {
	ctx := context.Background()
	base := newMockStore()
	config := []byte(`{"version":1,"created":"2024-01-01T00:00:00Z","encrypted":false}`)
	if err := base.Put(ctx, "config", config); err != nil {
		t.Fatalf("Failed to setup mock data: %v", err)
	}

	client, err := NewClient(context.Background(), base)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	_, err = client.Cat(ctx)
	if err == nil {
		t.Fatal("Expected error for no keys, got nil")
	}

	expectedMsg := "at least one object key is required"
	if err.Error() != expectedMsg {
		t.Errorf("Expected error %q, got %q", expectedMsg, err.Error())
	}
}

func TestClient_Cat_WithEncryption(t *testing.T) {
	ctx := context.Background()
	base := newMockStore()

	// Create a 32-byte encryption key
	encKey := make([]byte, 32)
	for i := range encKey {
		encKey[i] = byte(i)
	}

	// Add test data to base store (will be encrypted by client)
	config := core.RepoConfig{
		Version:   1,
		Created:   "2024-01-01T00:00:00Z",
		Encrypted: true,
	}
	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	// Create client with encryption - it will wrap the store
	client, err := NewClient(context.Background(), base, WithEncryptionKey(encKey))
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Put data through the encrypted client
	if err := client.Store().Put(ctx, "config", configData); err != nil {
		t.Fatalf("Failed to put config: %v", err)
	}

	// Retrieve it with Cat
	results, err := client.Cat(ctx, "config")
	if err != nil {
		t.Fatalf("Cat failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Verify the data is decrypted correctly
	var gotConfig core.RepoConfig
	if err := json.Unmarshal(results[0].Data, &gotConfig); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	if gotConfig.Version != config.Version {
		t.Errorf("Expected version %d, got %d", config.Version, gotConfig.Version)
	}
	if gotConfig.Encrypted != config.Encrypted {
		t.Errorf("Expected encrypted %v, got %v", config.Encrypted, gotConfig.Encrypted)
	}

	// Verify that the data in the base store is actually encrypted
	rawData, err := base.Get(ctx, "config")
	if err != nil {
		t.Fatalf("Failed to get raw data: %v", err)
	}

	// The raw data should be different from the plaintext (it's compressed + encrypted)
	if string(rawData) == string(configData) {
		t.Error("Data in base store should be encrypted/compressed, but appears to be plaintext")
	}
}

func TestClient_Cat_WithCompression(t *testing.T) {
	ctx := context.Background()
	base := newMockStore()
	config := []byte(`{"version":1,"created":"2024-01-01T00:00:00Z","encrypted":false}`)
	if err := base.Put(ctx, "config", config); err != nil {
		t.Fatalf("Failed to setup mock data: %v", err)
	}

	client, err := NewClient(context.Background(), base)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Create a large compressible object
	largeData := make(map[string]string)
	for i := 0; i < 100; i++ {
		largeData["key"+string(rune(i))] = "value_that_repeats_many_times"
	}
	largeJSON, err := json.Marshal(largeData)
	if err != nil {
		t.Fatalf("Failed to marshal large data: %v", err)
	}

	// Put through client (will be compressed)
	if err := client.Store().Put(ctx, "large", largeJSON); err != nil {
		t.Fatalf("Failed to put large object: %v", err)
	}

	// Retrieve with Cat
	results, err := client.Cat(ctx, "large")
	if err != nil {
		t.Fatalf("Cat failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Verify decompression worked
	var gotData map[string]string
	if err := json.Unmarshal(results[0].Data, &gotData); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	if len(gotData) != len(largeData) {
		t.Errorf("Expected %d keys, got %d", len(largeData), len(gotData))
	}

	// Verify the raw stored data is smaller (compressed)
	rawData, err := base.Get(ctx, "large")
	if err != nil {
		t.Fatalf("Failed to get raw data: %v", err)
	}

	if len(rawData) >= len(largeJSON) {
		t.Logf("Warning: Compressed data (%d bytes) not smaller than original (%d bytes)",
			len(rawData), len(largeJSON))
	}
}
