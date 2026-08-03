package engine

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/repoconfig"
	"github.com/cloudstic/cli/pkg/keychain"
)

// loadMarker reads and decodes the repository marker, unsealing it when needed.
func loadMarker(t *testing.T, s *MockStore, encryptionKey []byte) *core.RepoConfig {
	t.Helper()
	data, err := s.Get(context.Background(), "config")
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg, err := repoconfig.Decode(data, encryptionKey)
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}

func TestInitManager_AssignsRepositoryID(t *testing.T) {
	first := NewMockStore()
	if _, err := NewInitManager(Deps{Store: first}).Run(context.Background(), WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}
	second := NewMockStore()
	if _, err := NewInitManager(Deps{Store: second}).Run(context.Background(), WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}

	a := loadMarker(t, first, nil).ID
	b := loadMarker(t, second, nil).ID

	if a == "" || b == "" {
		t.Fatalf("init left the repository id empty (%q, %q)", a, b)
	}
	if a == b {
		t.Error("two repositories were initialized with the same id")
	}
}

func TestInitManager_AssignsRepositoryIDToEncryptedRepo(t *testing.T) {
	// The marker is sealed for an encrypted repository, so the id has to
	// survive a seal/unseal round trip rather than only a plain marshal.
	s := NewMockStore()
	chain := keychain.Chain{keychain.WithPassword("test-password")}
	if _, err := NewInitManager(Deps{Store: s}).Run(context.Background(), WithInitCredentials(chain)); err != nil {
		t.Fatalf("init: %v", err)
	}

	slots, err := keychain.LoadKeySlots(context.Background(), s)
	if err != nil {
		t.Fatalf("load key slots: %v", err)
	}
	masterKey, err := chain.Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("resolve master key: %v", err)
	}
	// The marker is sealed with the derived encryption key, not the master key
	// the slots yield directly.
	encryptionKey, err := keychain.DeriveEncryptionKey(masterKey)
	if err != nil {
		t.Fatalf("derive encryption key: %v", err)
	}

	if cfg := loadMarker(t, s, encryptionKey); cfg.ID == "" {
		t.Error("encrypted repository was initialized without an id")
	}
}

func TestInitManager_AdoptPreservesRepositoryID(t *testing.T) {
	// Re-initializing must not re-identify the repository. Snapshots copied
	// out of it elsewhere record the old id as provenance, and a new one would
	// make the next `copy` import the entire history again.
	s := NewMockStore()
	if _, err := NewInitManager(Deps{Store: s}).Run(context.Background(), WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}
	original := loadMarker(t, s, nil).ID

	if _, err := NewInitManager(Deps{Store: s}).Run(
		context.Background(), WithInitNoEncryption(), WithInitAdoptSlots(),
	); err != nil {
		t.Fatalf("re-init: %v", err)
	}

	if got := loadMarker(t, s, nil).ID; got != original {
		t.Errorf("id changed on adopt: %q -> %q", original, got)
	}
}

func TestInitManager_AdoptAssignsIDToLegacyRepository(t *testing.T) {
	// A repository written before RepoConfig.ID existed has no provenance to
	// invalidate, so adopting it may safely mint one.
	s := NewMockStore()
	legacy := core.RepoConfig{Version: 1, Created: "2026-01-01T00:00:00Z"}
	data, err := repoconfig.Encode(legacy, nil)
	if err != nil {
		t.Fatalf("encode legacy marker: %v", err)
	}
	if err := s.Put(context.Background(), "config", data); err != nil {
		t.Fatalf("seed legacy marker: %v", err)
	}

	if _, err := NewInitManager(Deps{Store: s}).Run(
		context.Background(), WithInitNoEncryption(), WithInitAdoptSlots(),
	); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	cfg := loadMarker(t, s, nil)
	if cfg.ID == "" {
		t.Error("adopting a legacy repository did not assign an id")
	}
	// The format floor must still not move down.
	if cfg.Version < 1 {
		t.Errorf("version floor moved down to %d", cfg.Version)
	}
}
