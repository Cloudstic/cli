package cloudstic

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	localstore "github.com/cloudstic/cli/pkg/store/local"

	"github.com/cloudstic/cli/pkg/source/local"
)

// The disk cache is off unless a directory is named, and both the directory
// and the budget arrive as arguments. Nothing in this package reads the
// environment for them: where they come from is cmd/cloudstic's question,
// resolved into a pkg/config.Client and applied by pkg/open, which is what
// every other knob here does.

// cacheClient builds a client whose object cache is dir, bounded at budget.
func cacheClient(t *testing.T, storeDir, dir string, budget int64, opts ...ClientOption) *Client {
	t.Helper()
	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	opts = append([]ClientOption{
		WithEncryptionKey(packfileTestKey()),
		WithObjectCache(dir, budget),
	}, opts...)
	client, err := NewClient(context.Background(), base, opts...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func TestNewClient_ObjectCacheIsOptIn(t *testing.T) {
	storeDir := t.TempDir()
	writeRepoConfig(t, storeDir)

	if c := newPackfileClient(t, storeDir); c.objectCache != nil {
		t.Error("a client built without WithObjectCache got one anyway")
	}
	if c := cacheClient(t, storeDir, "", 0); c.objectCache != nil {
		t.Error("an empty cache directory produced a cache")
	}
	if c := cacheClient(t, storeDir, t.TempDir(), 0); c.objectCache == nil {
		t.Error("a client given a cache directory got no cache")
	}
}

// The end of the chain the budget has to survive: real traffic through a real
// repository, with the bound measured on the directory rather than on the
// layer's own accounting.
func TestClient_ObjectCacheStaysWithinItsConfiguredBudget(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()
	cacheDir := t.TempDir()

	// Distinct, incompressible content per file, several times the budget in
	// total, so the restore below has to evict rather than merely fitting.
	// Incompressible matters: compression sits at the top of the chain and the
	// cache at the bottom, so what it holds is what a repetitive tree
	// compresses down to, which is nothing worth bounding.
	rng := rand.New(rand.NewSource(20260901))
	files := map[string]string{}
	for i := range 60 {
		body := make([]byte, 4<<10)
		if _, err := rng.Read(body); err != nil {
			t.Fatal(err)
		}
		files[filepath.Join("dir", fmt.Sprintf("f%02d.bin", i))] = string(body)
	}
	writeSourceTree(t, sourceDir, files)
	writeRepoConfig(t, storeDir)

	const budget = 32 << 10
	// Packfiles off, so the cache sees the individual objects rather than one
	// bundle larger than the whole budget — which it would decline outright
	// and cache nothing, proving nothing.
	client := cacheClient(t, storeDir, cacheDir, budget, WithPackfile(false))
	if client.objectCache == nil {
		t.Fatal("no object cache was wired despite a directory being given")
	}
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	restoreDir := t.TempDir()
	if _, err := client.RestoreToDir(ctx, restoreDir, "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertRestored(t, restoreDir, files)

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	var on int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		on += info.Size()
	}
	if on > budget {
		t.Errorf("cache directory holds %d bytes in %d entries, over its %d budget", on, len(entries), budget)
	}
	if len(entries) == 0 {
		t.Error("nothing was cached, so the budget was not the thing under test")
	}
}

// Check must read the repository, not a local copy of it, and the wiring that
// makes it do so lives in check.go. A cache directory that is still empty
// after a full verification is what proves the bypass reached the layer.
func TestClient_CheckDoesNotReadThroughTheObjectCache(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()

	writeSourceTree(t, sourceDir, map[string]string{"a.txt": "alpha", "nested/b.txt": "beta"})
	writeRepoConfig(t, storeDir)

	if _, err := newPackfileClientOrErr(storeDir); err != nil {
		t.Fatal(err)
	}
	warm := newPackfileClient(t, storeDir)
	if _, err := warm.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// A cache directory that has never held anything, opened after the
	// repository was written: anything in it afterwards came from the check.
	cacheDir := t.TempDir()
	client := cacheClient(t, storeDir, cacheDir, 0)
	if client.objectCache == nil {
		t.Fatal("no object cache was wired despite a directory being given")
	}
	res, err := client.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("a repository written by this build must verify clean: %v", res.Errors)
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("check populated the cache with %d entries; it must verify the store, not a copy of it", len(entries))
	}
}
