package cloudstic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	localstore "github.com/cloudstic/cli/pkg/store/local"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/source/local"
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

// rewindConfigVersion lowers the recorded format without disturbing the rest of
// the marker, standing in for a repository created by an earlier build.
func rewindConfigVersion(t *testing.T, s store.ObjectStore, version int) {
	t.Helper()
	ctx := context.Background()

	data, err := s.Get(ctx, "config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg core.RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Version = version
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "config", out); err != nil {
		t.Fatal(err)
	}
}

func newFormatTestStore(t *testing.T) store.ObjectStore {
	t.Helper()
	s, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The forward half of the compatibility contract: a repository written by a
// newer build must be refused, not partly understood. Misreading an index as
// empty is how a prune deletes a live repository.
func TestLoadRepoConfig_RefusesNewerFormat(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	writeConfigVersion(t, s, core.MaxSupportedRepoFormat+1)

	cfg, err := LoadRepoConfig(ctx, s, nil)
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

// The backward half: every version up to the supported maximum stays readable.
func TestLoadRepoConfig_AcceptsSupportedFormats(t *testing.T) {
	ctx := context.Background()
	for version := 1; version <= core.MaxSupportedRepoFormat; version++ {
		s := newFormatTestStore(t)
		writeConfigVersion(t, s, version)

		cfg, err := LoadRepoConfig(ctx, s, nil)
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
// version as "newer than supported" would strand the oldest repositories,
// which is the exact opposite of what the gate is for.
func TestLoadRepoConfig_AcceptsMissingVersion(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	if err := s.Put(ctx, "config", []byte(`{"created":"2026-01-01T00:00:00Z","encrypted":true}`)); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadRepoConfig(ctx, s, nil)
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

	cfg, err := LoadRepoConfig(ctx, s, nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a config after init")
		// t.Fatal already ends the test. The explicit return is for
		// staticcheck: on the linux/amd64 CI runner it does not model Fatal
		// as terminating here and reports the dereference below as SA5011,
		// though it does so on other platforms. Keep it.
		return
	}
	if cfg.Version != core.RepoFormatVersion {
		t.Errorf("init stamped version %d, want %d", cfg.Version, core.RepoFormatVersion)
	}
}

// The version this build writes must be one it can also read. A build that
// creates repositories it then refuses would be immediately broken.
func TestWrittenFormatIsSupported(t *testing.T) {
	if core.RepoFormatVersion > core.MaxSupportedRepoFormat {
		t.Fatalf("RepoFormatVersion (%d) exceeds MaxSupportedRepoFormat (%d): this build would refuse its own repositories",
			core.RepoFormatVersion, core.MaxSupportedRepoFormat)
	}
}

// Upgrading in place must raise the recorded version, so a repository that has
// been given newer-format content stops advertising itself as readable by
// builds that would misread it.
func TestUpgradeRepoFormat_RaisesVersion(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	writeConfigVersion(t, s, 1)

	// The fixture marker is an encrypted repository's, so raising its version
	// reseals it — exercising the unseal/reseal path, not just the rewrite.
	key := packfileTestKey()
	if err := UpgradeRepoFormat(ctx, s, core.MaxSupportedRepoFormat, key); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	cfg, err := LoadRepoConfig(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != core.MaxSupportedRepoFormat {
		t.Errorf("version = %d, want %d", cfg.Version, core.MaxSupportedRepoFormat)
	}
	// Fields other than the version must survive the rewrite.
	if !cfg.Encrypted {
		t.Error("upgrade dropped the encrypted flag")
	}
	if cfg.Created == "" {
		t.Error("upgrade dropped the created timestamp")
	}
}

// The version is a floor, never a ceiling: upgrading must not walk it back.
func TestUpgradeRepoFormat_NeverLowersVersion(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	writeConfigVersion(t, s, core.MaxSupportedRepoFormat)

	if err := UpgradeRepoFormat(ctx, s, 1, nil); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	cfg, err := LoadRepoConfig(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != core.MaxSupportedRepoFormat {
		t.Errorf("version was lowered to %d", cfg.Version)
	}
}

// A build must not stamp a version it cannot itself read, which would make the
// repository unopenable by the very build that upgraded it.
func TestUpgradeRepoFormat_RefusesUnsupportedTarget(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	writeConfigVersion(t, s, 1)

	if err := UpgradeRepoFormat(ctx, s, core.MaxSupportedRepoFormat+1, nil); err == nil {
		t.Fatal("expected a refusal to stamp an unsupported version")
	}

	cfg, err := LoadRepoConfig(ctx, s, nil)
	if err != nil {
		t.Fatalf("repository should remain readable after a refused upgrade: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("refused upgrade still changed the version to %d", cfg.Version)
	}
}

// The gate must hold on every path that opens a repository, including the
// library one. NewClient resolves the key from config only when no key was
// supplied, so gating just that branch would let WithEncryptionKey walk past
// the check — which is how the CLI stayed protected while the exported API did
// not.
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

// A backup that seals the pack index leaves the repository unreadable by builds
// that predate sealing, so the recorded format must have risen by the time the
// operation returns. Sealing happens inside the flush a backup performs, so the
// write-path stamp covers it without a separate hook.
func TestBackupThatSealsStampsTheFormat(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()
	writeSourceTree(t, sourceDir, map[string]string{"a.txt": "alpha"})

	writeRepoConfig(t, storeDir)

	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := LoadRepoConfig(ctx, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.Version != core.RepoFormatV2 {
		t.Fatalf("precondition: repository starts at %d, want %d", before.Version, core.RepoFormatV2)
	}

	client := newPackfileClient(t, storeDir)
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	after, err := LoadRepoConfig(ctx, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != core.MaxInPlaceUpgradeFormat {
		t.Errorf("after sealing the format is %d, want %d", after.Version, core.MaxInPlaceUpgradeFormat)
	}

	// And the repository it just stamped must still be one it can open.
	if _, err := newPackfileClientOrErr(storeDir); err != nil {
		t.Errorf("a build must be able to reopen a repository it stamped: %v", err)
	}
}

// A repository with no encryption never seals, so it must stay at the creation
// baseline. Stamping it would lock out builds that can read it perfectly well.
func TestUnsealedRepositoryKeepsBaselineFormat(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()
	writeSourceTree(t, sourceDir, map[string]string{"a.txt": "alpha"})

	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, base, WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}

	client, err := NewClient(ctx, base, WithPackfile(true))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	cfg, err := LoadRepoConfig(ctx, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != core.RepoFormatVersion {
		t.Errorf("an unencrypted repository was stamped %d, want %d",
			cfg.Version, core.RepoFormatVersion)
	}
}

// Writing to a repository created before this format tells the rest of the
// fleet to upgrade, because a heterogeneous set of writers is the dangerous
// state. Reading must not: a list or a restore that silently locked out another
// machine would be a surprising side effect of an innocuous command.
func TestWritesStampTheFormatButReadsDoNot(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()
	writeSourceTree(t, sourceDir, map[string]string{"a.txt": "alpha"})

	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, base, WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Rewind to an older format, as a repository created by an earlier build,
	// preserving everything else about the marker.
	rewindConfigVersion(t, base, 1)

	client, err := NewClient(ctx, base, WithPackfile(true))
	if err != nil {
		t.Fatal(err)
	}

	// A read must leave the recorded format alone.
	if _, err := client.List(ctx); err != nil {
		t.Fatalf("list: %v", err)
	}
	cfg, err := LoadRepoConfig(ctx, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Errorf("a read stamped the repository to %d; reads must not mutate it", cfg.Version)
	}

	// A write must raise it.
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}
	cfg, err = LoadRepoConfig(ctx, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != core.MaxInPlaceUpgradeFormat {
		t.Errorf("after a write the format is %d, want %d", cfg.Version, core.MaxInPlaceUpgradeFormat)
	}
}

// Adopting an existing repository rewrites its marker, and it reads that marker
// directly rather than through LoadRepoConfig — so the version gate has to be
// applied there too. Without it, `init --adopt-slots` on a repository this build
// cannot read would stamp it back down to a version this build understands,
// while leaving data it does not: a repository that failed safely becomes one
// that is silently misread.
func TestInitAdoptRefusesNewerFormat(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	if _, err := InitRepo(ctx, s, WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}
	writeConfigVersion(t, s, core.MaxSupportedRepoFormat+1)

	_, err := InitRepo(ctx, s, WithInitNoEncryption(), WithInitAdoptSlots())
	if err == nil {
		t.Fatal("adopting a repository newer than this build supports should be refused")
	}
	if !strings.Contains(err.Error(), "newer than this build supports") {
		t.Errorf("expected a format refusal, got: %v", err)
	}

	// And the marker must be untouched by the refusal.
	data, err := s.Get(ctx, "config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg core.RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Version != core.MaxSupportedRepoFormat+1 {
		t.Errorf("refused adopt changed the version to %d", cfg.Version)
	}
}

// The recorded format is a floor. Even a supported adopt must not walk it back,
// which it would if it simply stamped the build's own version.
func TestInitAdoptNeverLowersTheFormat(t *testing.T) {
	ctx := context.Background()
	s := newFormatTestStore(t)
	if _, err := InitRepo(ctx, s, WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}
	// A repository already at the highest version this build can read.
	writeConfigVersion(t, s, core.MaxSupportedRepoFormat)
	rewindEncrypted(t, s, false)

	if _, err := InitRepo(ctx, s, WithInitNoEncryption(), WithInitAdoptSlots()); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	cfg, err := LoadRepoConfig(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version < core.MaxSupportedRepoFormat {
		t.Errorf("adopt lowered the recorded format to %d", cfg.Version)
	}
}

// rewindEncrypted flips the encrypted flag without touching anything else.
func rewindEncrypted(t *testing.T, s store.ObjectStore, encrypted bool) {
	t.Helper()
	ctx := context.Background()

	data, err := s.Get(ctx, "config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg core.RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Encrypted = encrypted
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "config", out); err != nil {
		t.Fatal(err)
	}
}

// A preview must not mutate. Stamping the format on a dry run would make a
// read-only command lock other machines out of the repository — the exact harm
// the "writes stamp, reads do not" rule exists to prevent, arriving through an
// operation the user asked not to change anything.
func TestDryRunsDoNotStampTheFormat(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()
	writeSourceTree(t, sourceDir, map[string]string{"a.txt": "alpha"})

	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, base, WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}

	client, err := NewClient(ctx, base, WithPackfile(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Rewind, as a repository written by an earlier build.
	rewindConfigVersion(t, base, 1)

	t.Run("prune", func(t *testing.T) {
		if _, err := client.Prune(ctx, WithPruneDryRun()); err != nil {
			t.Fatalf("prune dry run: %v", err)
		}
		assertFormatVersion(t, base, 1)
	})

	t.Run("forget", func(t *testing.T) {
		if _, err := client.Forget(ctx, "latest", WithForgetDryRun()); err != nil {
			t.Fatalf("forget dry run: %v", err)
		}
		assertFormatVersion(t, base, 1)
	})

	// And a real mutation still stamps, so the guard is not simply disabling it.
	t.Run("a real prune still stamps", func(t *testing.T) {
		if _, err := client.Prune(ctx); err != nil {
			t.Fatalf("prune: %v", err)
		}
		assertFormatVersion(t, base, core.MaxInPlaceUpgradeFormat)
	})
}

// ForgetPolicy mutates like Forget does, so it has to stamp like Forget does.
// It was missed when the other write paths were wired up.
func TestForgetPolicyStampsTheFormat(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()
	writeSourceTree(t, sourceDir, map[string]string{"a.txt": "alpha"})

	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, base, WithInitNoEncryption()); err != nil {
		t.Fatalf("init: %v", err)
	}
	client, err := NewClient(ctx, base, WithPackfile(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}
	rewindConfigVersion(t, base, 1)

	if _, err := client.ForgetPolicy(ctx, WithKeepLast(1)); err != nil {
		t.Fatalf("forget policy: %v", err)
	}
	assertFormatVersion(t, base, core.MaxInPlaceUpgradeFormat)
}

func assertFormatVersion(t *testing.T, s store.ObjectStore, want int) {
	t.Helper()
	cfg, err := LoadRepoConfig(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Version != want {
		t.Errorf("recorded format is %d, want %d", cfg.Version, want)
	}
}
