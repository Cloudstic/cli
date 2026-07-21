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

// These are integration tests over the real client: the full store chain
// (Compressed → Encrypted → Metered → Pack → backend), a real backup, and a
// real restore. They live here rather than in e2e because reproducing a
// pre-sealing repository requires the master key, which the CLI deliberately
// never exposes.

func migrationTestKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 7)
	}
	return key
}

// newMigrationRepo returns a client over a local store at dir, configured the
// way the CLI configures an encrypted repository with packfiles enabled.
func newMigrationRepo(t *testing.T, dir string) *Client {
	t.Helper()
	base, err := store.NewLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(context.Background(), base,
		WithEncryptionKey(migrationTestKey()),
		WithPackfile(true),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

// downgradeCatalogToPlaintext rewrites index/packs in the pre-sealing format,
// reproducing a repository written by a client that did not seal its indexes.
func downgradeCatalogToPlaintext(t *testing.T, storeDir string) {
	t.Helper()
	ctx := context.Background()

	base, err := store.NewLocalStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := base.Get(ctx, "index/packs")
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if !crypto.IsEncrypted(sealed) {
		t.Fatal("precondition: expected a sealed catalog to downgrade")
	}

	indexKey, err := crypto.DeriveKey(migrationTestKey(), crypto.HKDFInfoPackIndexV1)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := crypto.Decrypt(sealed, indexKey)
	if err != nil {
		t.Fatalf("decrypt catalog: %v", err)
	}
	// Sanity: the plaintext really is the catalog JSON a legacy client wrote.
	var catalog map[string]any
	if err := json.Unmarshal(plaintext, &catalog); err != nil {
		t.Fatalf("catalog plaintext is not JSON: %v", err)
	}
	if len(catalog) == 0 {
		t.Fatal("catalog is empty; the test is not exercising the pack path")
	}

	if err := base.Put(ctx, "index/packs", plaintext); err != nil {
		t.Fatalf("write plaintext catalog: %v", err)
	}
}

// writeRepoConfig writes the repository marker the engine expects. Init proper
// goes through key slots; these tests supply the master key directly.
func writeRepoConfig(t *testing.T, client *Client) {
	t.Helper()
	cfg := core.RepoConfig{Version: 1, Created: "2026-01-01T00:00:00Z", Encrypted: true}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Store().Put(context.Background(), "config", data); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func restoreLatest(ctx context.Context, client *Client, outDir string) error {
	_, err := client.RestoreToDir(ctx, outDir, "latest")
	return err
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

// A repository whose catalog predates sealing must remain fully usable: it
// restores correctly, accepts new backups, and its catalog is sealed again on
// the next flush without losing the entries the legacy client wrote.
func TestIntegration_PackIndexMigratesFromPlaintext(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()

	firstGen := map[string]string{
		"invoice.txt":       "classified invoice data",
		"subdir/nested.txt": "nested content",
	}
	writeSourceTree(t, sourceDir, firstGen)

	client := newMigrationRepo(t, storeDir)
	writeRepoConfig(t, client)
	if _, err := client.Backup(ctx, source.NewLocalSource(sourceDir)); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	// Rewind the catalog to the pre-sealing format.
	downgradeCatalogToPlaintext(t, storeDir)

	// A new client must read the legacy catalog and restore from it.
	legacyReader := newMigrationRepo(t, storeDir)
	restoreDir := t.TempDir()
	if err := restoreLatest(ctx, legacyReader, restoreDir); err != nil {
		t.Fatalf("restore from legacy catalog: %v", err)
	}
	assertRestored(t, restoreDir, firstGen)

	// A subsequent backup must re-seal the catalog and keep both generations.
	writeSourceTree(t, sourceDir, map[string]string{"second.txt": "second generation"})
	writer := newMigrationRepo(t, storeDir)
	if _, err := writer.Backup(ctx, source.NewLocalSource(sourceDir)); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	base, err := store.NewLocalStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := base.Get(ctx, "index/packs")
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncrypted(after) {
		t.Error("catalog should be sealed again after a backup by a sealing client")
	}

	final := newMigrationRepo(t, storeDir)
	finalDir := t.TempDir()
	if err := restoreLatest(ctx, final, finalDir); err != nil {
		t.Fatalf("restore after migration: %v", err)
	}
	want := map[string]string{"second.txt": "second generation"}
	for k, v := range firstGen {
		want[k] = v
	}
	assertRestored(t, finalDir, want)
}

// The same repository, read end to end with sealing active from the start.
func TestIntegration_SealedPackIndexRoundTrip(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()

	files := map[string]string{
		"a.txt":        "alpha",
		"nested/b.txt": "beta",
	}
	writeSourceTree(t, sourceDir, files)

	client := newMigrationRepo(t, storeDir)
	writeRepoConfig(t, client)
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
		t.Fatal("catalog should be sealed")
	}

	reader := newMigrationRepo(t, storeDir)
	out := t.TempDir()
	if err := restoreLatest(ctx, reader, out); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertRestored(t, out, files)
}
