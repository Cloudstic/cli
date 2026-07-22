package cloudstic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

func writeConfigVersion(t *testing.T, s store.ObjectStore, version int) {
	t.Helper()
	cfg := core.RepoConfig{Version: version, Created: "2026-01-01T00:00:00Z", Encrypted: true}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), "config", data); err != nil {
		t.Fatal(err)
	}
}

func newFormatTestStore(t *testing.T) store.ObjectStore {
	t.Helper()
	s, err := store.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A repository written by a newer build must be refused, not partly understood.
// This release cannot read a sealed pack index, and misreading an index as
// empty is how a prune deletes a live repository.
func TestLoadRepoConfig_RefusesNewerFormat(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	writeConfigVersion(t, s, core.MaxSupportedRepoFormat+1)

	cfg, err := LoadRepoConfig(ctx, s)
	if err == nil {
		t.Fatalf("expected a refusal, got config %+v", cfg)
	}
	if cfg != nil {
		t.Errorf("no config should be returned alongside the error, got %+v", cfg)
	}
	for _, want := range []string{"newer than this build supports", "upgrade cloudstic"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q, got: %v", want, err)
		}
	}
}

// Every version up to the supported maximum stays readable.
func TestLoadRepoConfig_AcceptsSupportedFormats(t *testing.T) {
	ctx := context.Background()
	for version := 1; version <= core.MaxSupportedRepoFormat; version++ {
		s := newFormatTestStore(t)
		writeConfigVersion(t, s, version)

		cfg, err := LoadRepoConfig(ctx, s)
		if err != nil {
			t.Errorf("format version %d should be readable: %v", version, err)
			continue
		}
		if cfg == nil || cfg.Version != version {
			t.Errorf("version %d: got %+v", version, cfg)
		}
	}
}

// A config with no version field must not be refused. Treating a missing
// version as "newer than supported" would strand the oldest repositories.
func TestLoadRepoConfig_AcceptsMissingVersion(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	if err := s.Put(ctx, "config", []byte(`{"created":"2026-01-01T00:00:00Z","encrypted":true}`)); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadRepoConfig(ctx, s)
	if err != nil {
		t.Fatalf("a config without a version must still load: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a config")
	}
}

// New repositories must be stamped with the version this build writes, or the
// gate has nothing to act on.
func TestInitStampsCurrentFormatVersion(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)

	if _, err := InitRepo(ctx, s, WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg, err := LoadRepoConfig(ctx, s)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a config after init")
	}
	if cfg.Version != core.RepoFormatVersion {
		t.Errorf("init stamped version %d, want %d", cfg.Version, core.RepoFormatVersion)
	}
}

// A build that creates repositories it then refuses would be broken on arrival.
func TestWrittenFormatIsSupported(t *testing.T) {
	if core.RepoFormatVersion > core.MaxSupportedRepoFormat {
		t.Fatalf("RepoFormatVersion (%d) exceeds MaxSupportedRepoFormat (%d): this build would refuse its own repositories",
			core.RepoFormatVersion, core.MaxSupportedRepoFormat)
	}
}

// The gate must hold on every path that opens a repository, including the
// library one. NewClient resolves the key from config only when no key was
// supplied, so gating just that branch would let WithEncryptionKey walk past
// the check.
func TestNewClient_RefusesNewerFormat(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		opts []ClientOption
	}{
		{name: "explicit encryption key", opts: []ClientOption{WithEncryptionKey(make([]byte, 32))}},
		{name: "key resolved from config", opts: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFormatTestStore(t)
			writeConfigVersion(t, s, core.MaxSupportedRepoFormat+1)

			if _, err := NewClient(ctx, s, tc.opts...); err == nil {
				t.Fatal("NewClient accepted a repository newer than this build supports")
			} else if !strings.Contains(err.Error(), "newer than this build supports") {
				t.Errorf("expected a format refusal, got: %v", err)
			}
		})
	}
}
