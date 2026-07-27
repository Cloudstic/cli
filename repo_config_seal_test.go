package cloudstic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/repoconfig"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/store"
)

// sealTestPassword is the credential every repository in this file is created
// with, so a test can re-derive the encryption key the way a real open does.
const sealTestPassword = "seal-test-pass"

func sealTestChain() keychain.Chain {
	return keychain.Chain{keychain.WithPassword(sealTestPassword)}
}

// sealTestKey resolves the repository encryption key from the key slots, which
// is what a caller needing to read a sealed marker has to do.
func sealTestKey(t *testing.T, s store.ObjectStore) []byte {
	t.Helper()
	slots, err := keychain.LoadKeySlots(context.Background(), s)
	if err != nil {
		t.Fatalf("load key slots: %v", err)
	}
	masterKey, err := sealTestChain().Resolve(context.Background(), slots)
	if err != nil {
		t.Fatalf("resolve master key: %v", err)
	}
	key, err := keychain.DeriveEncryptionKey(masterKey)
	if err != nil {
		t.Fatalf("derive encryption key: %v", err)
	}
	return key
}

func newSealedRepo(t *testing.T) store.ObjectStore {
	t.Helper()
	s := newFormatTestStore(t)
	if _, err := InitRepo(context.Background(), s, WithInitCredentials(sealTestChain())); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	return s
}

// The marker of an encrypted repository must not be readable from the stored
// bytes: that is the whole point of sealing it rather than authenticating it.
func TestSealedConfig_ContentsAreNotReadableWithoutKey(t *testing.T) {
	ctx := context.Background()
	s := newSealedRepo(t)

	raw, err := s.Get(ctx, "config")
	if err != nil {
		t.Fatal(err)
	}
	if !repoconfig.IsSealed(raw) {
		t.Fatal("expected the marker to be sealed")
	}
	var probe core.RepoConfig
	if err := json.Unmarshal(raw, &probe); err == nil {
		t.Error("sealed marker should not parse as JSON")
	}
	if strings.Contains(string(raw), "version") {
		t.Error("sealed marker still exposes its field names")
	}

	cfg, err := LoadRepoConfig(ctx, s, sealTestKey(t, s))
	if err != nil {
		t.Fatalf("reading with the key should succeed: %v", err)
	}
	if !cfg.Encrypted || cfg.Version != core.RepoFormatVersion {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

// Editing a sealed marker must be caught. GCM authenticates the whole object,
// so this is the property that closes the version/encrypted downgrade.
func TestSealedConfig_TamperingIsDetected(t *testing.T) {
	ctx := context.Background()
	s := newSealedRepo(t)
	key := sealTestKey(t, s)

	raw, err := s.Get(ctx, "config")
	if err != nil {
		t.Fatal(err)
	}
	tampered := make([]byte, len(raw))
	copy(tampered, raw)
	// Flip a bit inside the sealed body, past the version byte and nonce.
	tampered[crypto.Overhead] ^= 0x01
	if err := s.Put(ctx, "config", tampered); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadRepoConfig(ctx, s, key); err == nil {
		t.Fatal("expected a tampered marker to be refused")
	}
	if _, err := NewClient(ctx, s, WithKeychain(sealTestChain())); err == nil {
		t.Fatal("expected opening a repository with a tampered marker to fail")
	}
}

// Replacing a sealed marker with a plaintext one claiming no encryption is the
// downgrade sealing cannot detect on its own, because a plaintext marker is
// also what every legacy repository has. The surviving key slots are what give
// it away, and the check needs no key.
func TestPlaintextConfigWithKeySlotsIsRefused(t *testing.T) {
	ctx := context.Background()
	s := newSealedRepo(t)

	downgraded, err := json.Marshal(core.RepoConfig{
		Version:   core.RepoFormatVersion,
		Created:   "2026-01-01T00:00:00Z",
		Encrypted: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "config", downgraded); err != nil {
		t.Fatal(err)
	}

	_, err = NewClient(ctx, s, WithKeychain(sealTestChain()))
	if err == nil {
		t.Fatal("expected a repository claiming no encryption with key slots present to be refused")
	}
	if !strings.Contains(err.Error(), "key slots") {
		t.Errorf("error should name the contradiction, got: %v", err)
	}
}

// Backward compatibility is permanent: an encrypted repository written before
// sealing existed has a plaintext marker and must stay readable.
func TestLegacyPlaintextConfigStillOpens(t *testing.T) {
	ctx := context.Background()
	s := newSealedRepo(t)

	// Rewrite the marker the way an older build would have: plaintext, with
	// the key slots left in place.
	legacy, err := json.Marshal(core.RepoConfig{
		Version:   1,
		Created:   "2026-01-01T00:00:00Z",
		Encrypted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "config", legacy); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadRepoConfig(ctx, s, nil)
	if err != nil {
		t.Fatalf("a legacy plaintext marker must still be readable: %v", err)
	}
	if !cfg.Encrypted || cfg.Version != 1 {
		t.Errorf("unexpected legacy config: %+v", cfg)
	}

	if _, err := NewClient(ctx, s, WithKeychain(sealTestChain())); err != nil {
		t.Fatalf("a legacy repository must still open: %v", err)
	}
}

// Raising the format of a legacy repository seals its marker on the way past,
// which is how a repository written before sealing acquires it.
func TestUpgradeSealsALegacyMarker(t *testing.T) {
	ctx := context.Background()
	s := newSealedRepo(t)
	key := sealTestKey(t, s)

	legacy, err := json.Marshal(core.RepoConfig{
		Version:   1,
		Created:   "2026-01-01T00:00:00Z",
		Encrypted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "config", legacy); err != nil {
		t.Fatal(err)
	}

	if err := UpgradeRepoFormat(ctx, s, core.RepoFormatVersion, key); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	raw, err := s.Get(ctx, "config")
	if err != nil {
		t.Fatal(err)
	}
	if !repoconfig.IsSealed(raw) {
		t.Error("raising the format should have sealed the marker")
	}
	cfg, err := LoadRepoConfig(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != core.RepoFormatVersion {
		t.Errorf("version = %d, want %d", cfg.Version, core.RepoFormatVersion)
	}
	if cfg.Created != "2026-01-01T00:00:00Z" {
		t.Error("resealing dropped a field it should have carried over")
	}
}

// An unencrypted repository has no key to seal with, so its marker stays
// plaintext and readable — and InspectRepo says so without one.
func TestUnencryptedConfigStaysPlaintext(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	if _, err := InitRepo(ctx, s, WithInitNoEncryption()); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	raw, err := s.Get(ctx, "config")
	if err != nil {
		t.Fatal(err)
	}
	if repoconfig.IsSealed(raw) {
		t.Fatal("an unencrypted repository has no key to seal its marker with")
	}

	status, err := InspectRepo(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Initialized || status.Encrypted || status.Sealed {
		t.Errorf("unexpected status: %+v", status)
	}
}
