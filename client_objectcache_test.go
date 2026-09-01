package cloudstic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"

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

// canary is content distinctive enough that finding it in a file is proof, and
// long enough that compression cannot coincidentally reproduce it.
const canary = "CLOUDSTIC-PLAINTEXT-CANARY-b6f1e2a4-DO-NOT-LEAK-THIS-STRING"

// storeObjects reads every object the backend holds, as raw bytes.
func storeObjects(t *testing.T, dir string) [][]byte {
	t.Helper()
	var objects [][]byte
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(body) > 0 {
			objects = append(objects, body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk store: %v", err)
	}
	return objects
}

// assertCacheMirrorsTheStore checks that every cache entry carries, verbatim,
// the bytes of some object the store holds.
//
// This is the invariant, and it is deliberately stronger than "no plaintext
// appears". A cache sitting above EncryptedStore but below CompressedStore
// holds zstd-compressed bodies, in which a canary string does not appear as a
// literal — so a search for plaintext passes while every user's decrypted data
// sits on disk. Verified: moving the layer up one place in NewClient leaves a
// canary search green and fails this.
//
// Byte identity with the store is what "the cache holds ciphertext" actually
// means. It is exact, it needs no knowledge of the entry header, and it fails
// for a cache placed anywhere above the backend, not just above encryption.
func assertCacheMirrorsTheStore(t *testing.T, cacheDir, storeDir string) {
	t.Helper()
	objects := storeObjects(t, storeDir)
	if len(objects) == 0 {
		t.Fatal("the store is empty, so this asserts nothing")
	}

	entries, matched := 0, 0
	err := filepath.WalkDir(cacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		entries++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The entry is a header of block hashes followed by the object, so the
		// object's bytes are a substring rather than the whole file.
		for _, obj := range objects {
			if bytes.Contains(body, obj) {
				matched++
				return nil
			}
		}
		t.Errorf("cache entry %s holds bytes that appear in no stored object; "+
			"DiskCacheStore must sit directly above the backend, below EncryptedStore",
			filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk cache: %v", err)
	}
	if entries == 0 {
		t.Fatal("the cache is empty, so this asserts nothing")
	}
	if matched != entries {
		t.Errorf("%d of %d cache entries mirror a stored object", matched, entries)
	}
}

// cacheHoldsCanary reports whether any entry in dir contains the canary, and
// how many entries were examined.
//
// Kept alongside the identity check as a direct statement of the consequence,
// not as the proof: on its own it is too weak, since compression hides a
// literal string just as well as encryption does.
func cacheHoldsCanary(t *testing.T, dir string) (found bool, entries int) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		entries++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte(canary)) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cache dir: %v", err)
	}
	return found, entries
}

// The cache writes repository bytes to a second location on the user's disk,
// and the claim that those bytes are ciphertext rests entirely on where
// DiskCacheStore sits: below EncryptedStore, so what it sees was sealed before
// it arrived. That is a chain-order property, which no unit test on the layer
// can observe — it has to be asserted through a real client that assembled the
// chain, over a real backup.
//
// It is worth pinning because the failure is silent and now affects everyone:
// the cache is on by default, so a refactor that moved the layer one place up
// would put every user's plaintext on disk with nothing failing.
func TestClient_ObjectCacheHoldsNoPlaintextFromAnEncryptedRepository(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()
	cacheDir := t.TempDir()

	// Repeated, so the file is large enough to be worth caching and so
	// compression cannot make the canary vanish by luck rather than by
	// encryption — a single occurrence surviving would be enough to fail.
	body := strings.Repeat(canary+"\n", 200)
	files := map[string]string{}
	for i := range 12 {
		files[fmt.Sprintf("secret%02d.txt", i)] = fmt.Sprintf("%s\n%d", body, i)
	}
	writeSourceTree(t, sourceDir, files)
	writeRepoConfig(t, storeDir)

	// Packfiles off so the restore fetches whole objects, which the cache
	// stores on sight. Bundled, the same reads are ranged and land in the
	// cache only once one repeats, which a small tree need not do — and a test
	// that silently caches nothing asserts nothing. The layering being pinned
	// is identical either way: PackStore and DiskCacheStore both sit below
	// EncryptedStore.
	client := cacheClient(t, storeDir, cacheDir, 0, WithPackfile(false))
	if client.objectCache == nil {
		t.Fatal("no object cache was wired")
	}
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}
	// Restore, so reads populate the cache: a backup mostly writes.
	restoreDir := t.TempDir()
	if _, err := client.RestoreToDir(ctx, restoreDir, "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertRestored(t, restoreDir, files)

	// The canary must be findable at all, or the search proves nothing.
	if !bytes.Contains([]byte(body), []byte(canary)) {
		t.Fatal("precondition: the canary is not in the source content")
	}
	assertCacheMirrorsTheStore(t, cacheDir, storeDir)

	if found, entries := cacheHoldsCanary(t, cacheDir); found {
		t.Errorf("the object cache holds source plaintext across %d entries", entries)
	}
}

// The guarantee stated exactly: the cache inherits the repository's
// encryption and adds none of its own. An unencrypted repository's objects are
// plaintext in the store, so they are plaintext in the cache too — which is not
// a leak, but is worth pinning so the claim above is never read as "the cache
// encrypts", and so anyone changing this sees which property they are relying
// on.
func TestClient_ObjectCacheInheritsTheRepositorysEncryptionAndAddsNone(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()
	cacheDir := t.TempDir()

	body := strings.Repeat(canary+"\n", 200)
	files := map[string]string{}
	for i := range 12 {
		files[fmt.Sprintf("open%02d.txt", i)] = fmt.Sprintf("%s\n%d", body, i)
	}
	writeSourceTree(t, sourceDir, files)

	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Put(ctx, "config", unencryptedRepoConfig(t)); err != nil {
		t.Fatal(err)
	}
	// No WithEncryptionKey: nothing in this repository is sealed.
	client, err := NewClient(ctx, base, WithObjectCache(cacheDir, 0), WithPackfile(false))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}
	restoreDir := t.TempDir()
	if _, err := client.RestoreToDir(ctx, restoreDir, "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}

	_, entries := cacheHoldsCanary(t, cacheDir)
	if entries == 0 {
		t.Fatal("the cache is empty, so this asserts nothing")
	}
	// Deliberately no assertion on `found`. Compression sits above the cache,
	// so whether the canary survives as a literal byte sequence depends on
	// zstd rather than on any property worth pinning. What is pinned is the
	// contrast with the test above: there, plaintext is impossible; here, it
	// is merely not promised.
}

func unencryptedRepoConfig(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(core.RepoConfig{
		Version:   core.RepoFormatVersion,
		Created:   "2026-01-01T00:00:00Z",
		Encrypted: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
