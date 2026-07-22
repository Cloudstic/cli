package cloudstic

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/source"
	"github.com/cloudstic/cli/pkg/store"
)

// Cloudstic is consumed as a library as well as a CLI, and this is the only
// coverage of a backup/restore round trip through the public Client with
// packfiles and encryption both enabled. The e2e suite cannot stand in for it:
// those tests drive the built binary, so they exercise cmd/cloudstic rather
// than the exported API a library user calls.
//
// Backward compatibility with pre-sealing repositories is covered where it
// belongs, against a real v1.14.0 repository, in
// e2e/feature_legacy_repo_test.go.

func packfileTestKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 7)
	}
	return key
}

// newPackfileClient returns a client configured the way the CLI configures an
// encrypted repository with packfiles enabled.
func newPackfileClient(t *testing.T, dir string) *Client {
	t.Helper()
	base, err := store.NewLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(context.Background(), base,
		WithEncryptionKey(packfileTestKey()),
		WithPackfile(true),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

// writeRepoConfig writes the repository marker the way init does: unencrypted,
// straight to the backing store. Init proper resolves the master key through
// key slots; these tests supply it outright.
//
// It must not go through the client chain — EncryptedStore would seal it, and
// the marker has to be readable before any key is available.
func writeRepoConfig(t *testing.T, dir string) {
	t.Helper()
	base, err := store.NewLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := core.RepoConfig{Version: core.RepoFormatVersion, Created: "2026-01-01T00:00:00Z", Encrypted: true}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Put(context.Background(), "config", data); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func writeSourceTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertRestored(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("restored %s: %v", name, err)
			continue
		}
		if string(got) != content {
			t.Errorf("restored %s = %q, want %q", name, got, content)
		}
	}
}

// A library user backing up and restoring through the Client, with packfiles
// and encryption on, must get their files back — and the pack index they leave
// behind must be sealed.
func TestClient_PackfileBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()

	files := map[string]string{
		"a.txt":        "alpha",
		"nested/b.txt": "beta",
	}
	writeSourceTree(t, sourceDir, files)

	writeRepoConfig(t, storeDir)
	client := newPackfileClient(t, storeDir)
	if _, err := client.Backup(ctx, source.NewLocalSource(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	base, err := store.NewLocalStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := base.Get(ctx, "index/packs")
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncrypted(catalog) {
		t.Error("the pack catalog written through the Client should be sealed")
	}

	reader := newPackfileClient(t, storeDir)
	out := t.TempDir()
	if _, err := reader.RestoreToDir(ctx, out, "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertRestored(t, out, files)
}
