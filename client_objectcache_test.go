package cloudstic

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	localstore "github.com/cloudstic/cli/pkg/store/local"

	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/pkg/source/local"
)

// The disk cache is off unless a directory is named, and its budget comes from
// the environment. Both are read here, at construction, and nowhere else — a
// value consulted per operation would be a syscall on a path that runs once
// per object (see internal/hamt/tuning.go on how that was learned).

// unsetEnv removes name for the duration of the test. There is no t.Unsetenv,
// so t.Setenv goes first purely to register the restore the harness does at
// cleanup — the value it sets is discarded a line later.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
}

func TestObjectCacheBudget_DefaultsAndRejectsNonsense(t *testing.T) {
	cases := []struct {
		name  string
		value string
		set   bool
		want  int64
	}{
		{name: "unset", want: storelayer.DiskCacheBudget},
		{name: "empty", value: "", set: true, want: storelayer.DiskCacheBudget},
		{name: "a size", value: "1048576", set: true, want: 1 << 20},
		// A typo, a unit suffix and a negative number are all the same thing:
		// no statement about how much disk may be spent. They fall back to the
		// default rather than to "no bound", because an unbounded cache
		// directory is the failure the budget exists to prevent and a typo is
		// not consent to it.
		{name: "malformed", value: "512MB", set: true, want: storelayer.DiskCacheBudget},
		{name: "zero", value: "0", set: true, want: storelayer.DiskCacheBudget},
		{name: "negative", value: "-1", set: true, want: storelayer.DiskCacheBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, envObjectCacheBytes)
			if tc.set {
				t.Setenv(envObjectCacheBytes, tc.value)
			}
			if got := objectCacheBudget(); got != tc.want {
				t.Errorf("objectCacheBudget() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNewClient_ObjectCacheIsOptIn(t *testing.T) {
	storeDir := t.TempDir()
	writeRepoConfig(t, storeDir)

	unsetEnv(t, envObjectCacheDir)
	if c := newPackfileClient(t, storeDir); c.objectCache != nil {
		t.Error("a client built with no cache directory got one anyway")
	}

	t.Setenv(envObjectCacheDir, t.TempDir())
	if c := newPackfileClient(t, storeDir); c.objectCache == nil {
		t.Error("a client built with a cache directory got no cache")
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
	t.Setenv(envObjectCacheDir, cacheDir)
	t.Setenv(envObjectCacheBytes, "32768")

	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	// Packfiles off, so the cache sees the individual objects rather than one
	// bundle larger than the whole budget — which it would decline outright
	// and cache nothing, proving nothing.
	client, err := NewClient(ctx, base, WithEncryptionKey(packfileTestKey()), WithPackfile(false))
	if err != nil {
		t.Fatal(err)
	}
	if client.objectCache == nil {
		t.Fatal("no object cache was wired despite a directory being set")
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
	t.Setenv(envObjectCacheDir, cacheDir)
	client := newPackfileClient(t, storeDir)
	if client.objectCache == nil {
		t.Fatal("no object cache was wired despite a directory being set")
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
