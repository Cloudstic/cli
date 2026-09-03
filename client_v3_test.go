package cloudstic

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/pkg/source/local"
	"github.com/cloudstic/cli/pkg/store"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// newV3Repo initializes a format-v3 repository in dir and returns a client
// opened on it. Unencrypted, so the on-disk namespaces can be asserted
// directly; the encrypted variant is covered separately.
func newV3Repo(t *testing.T, dir string) *Client {
	t.Helper()
	ctx := context.Background()
	base, err := localstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, base, WithInitFormat(core.RepoFormatV3)); err != nil {
		t.Fatalf("init v3: %v", err)
	}
	return openV3Repo(t, dir)
}

func openV3Repo(t *testing.T, dir string) *Client {
	t.Helper()
	base, err := localstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(context.Background(), base)
	if err != nil {
		t.Fatalf("open v3 client: %v", err)
	}
	return client
}

// listPrefix lists the raw backend keys under prefix, bypassing the client's
// store chain — the assertions below are about what physically exists.
func listPrefix(t *testing.T, dir, prefix string) []string {
	t.Helper()
	base, err := localstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := base.List(context.Background(), prefix)
	if err != nil {
		t.Fatalf("list %s: %v", prefix, err)
	}
	return keys
}

// TestClientV3_FullLifecycle drives a format-v3 repository through the whole
// pipeline — init, backup, incremental backup, ls, diff, find, check, restore,
// prune — and asserts the format's central claim along the way: no filemeta/,
// content/, or pack objects ever exist, because metadata and small content
// live in the leaves.
func TestClientV3_FullLifecycle(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()

	// A small tree plus one file large enough to be chunked (the inline
	// threshold is 512 KiB), so both content paths are exercised.
	big := make([]byte, 600*1024)
	rand.New(rand.NewSource(42)).Read(big)
	files := map[string]string{
		"a.txt":         "alpha",
		"nested/b.txt":  "beta",
		"nested/c.md":   "gamma gamma",
		"deep/d/e.conf": "delta",
	}
	writeSourceTree(t, sourceDir, files)
	if err := os.WriteFile(filepath.Join(sourceDir, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	client := newV3Repo(t, storeDir)

	// --- Backup 1 ---
	res, err := client.Backup(ctx, local.New(sourceDir))
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res.SnapshotRef == "" {
		t.Fatal("backup returned no snapshot ref")
	}

	// The format's claim, checked physically: nothing standalone but nodes,
	// chunks, the snapshot, and the mutable index keys.
	for _, prefix := range []string{"filemeta/", "content/", "packs/", "index/packs", "index/packmap/"} {
		if keys := listPrefix(t, storeDir, prefix); len(keys) != 0 {
			t.Errorf("v3 repository has %d objects under %s: %v", len(keys), prefix, keys[:min(3, len(keys))])
		}
	}
	if keys := listPrefix(t, storeDir, "node/"); len(keys) == 0 {
		t.Error("v3 repository has no node objects")
	}
	if keys := listPrefix(t, storeDir, "chunk/"); len(keys) == 0 {
		t.Error("v3 repository has no chunk objects for the large file")
	}

	// --- Restore 1 ---
	out1 := t.TempDir()
	if _, err := openV3Repo(t, storeDir).RestoreToDir(ctx, out1, "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertRestored(t, out1, files)
	gotBig, err := os.ReadFile(filepath.Join(out1, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBig, big) {
		t.Fatal("restored big.bin differs from the source")
	}

	// --- Check (read-data) ---
	checkRes, err := openV3Repo(t, storeDir).Check(ctx, WithReadData())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(checkRes.Errors) != 0 {
		t.Fatalf("check reported %d errors: %v", len(checkRes.Errors), checkRes.Errors)
	}

	// --- Incremental backup: change one file, add one, remove one ---
	files["a.txt"] = "alpha v2"
	files["new.txt"] = "fresh"
	writeSourceTree(t, sourceDir, files)
	if err := os.Remove(filepath.Join(sourceDir, "nested", "c.md")); err != nil {
		t.Fatal(err)
	}
	delete(files, "nested/c.md")

	client2 := openV3Repo(t, storeDir)
	res2, err := client2.Backup(ctx, local.New(sourceDir))
	if err != nil {
		t.Fatalf("incremental backup: %v", err)
	}
	if res2.FilesChanged == 0 || res2.FilesNew == 0 {
		t.Errorf("incremental stats: changed=%d new=%d, want both > 0", res2.FilesChanged, res2.FilesNew)
	}
	if res2.FilesUnmodified == 0 {
		t.Errorf("incremental saw no unmodified files; change detection is broken")
	}

	// --- Ls ---
	lsRes, err := client2.LsSnapshot(ctx, "latest")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if len(lsRes.RefToMeta) == 0 {
		t.Fatal("ls returned no entries")
	}

	// --- Diff between the two snapshots ---
	diffRes, err := client2.Diff(ctx, res.SnapshotRef, res2.SnapshotRef)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	var added, modified, removed int
	for _, ch := range diffRes.Changes {
		switch ch.Type {
		case ChangeAdded:
			added++
		case ChangeModified:
			modified++
		case ChangeRemoved:
			removed++
		}
	}
	if added == 0 || modified == 0 || removed == 0 {
		t.Errorf("diff: added=%d modified=%d removed=%d, want all > 0", added, modified, removed)
	}

	// --- Find ---
	findRes, err := client2.Find(ctx, FindQuery{Name: "big.bin"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(findRes.Matches) == 0 {
		t.Fatal("find matched nothing for big.bin")
	}

	// --- Prune with both snapshots live: reachable data must survive ---
	if _, err := client2.Prune(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}
	out2 := t.TempDir()
	if _, err := openV3Repo(t, storeDir).RestoreToDir(ctx, out2, "latest"); err != nil {
		t.Fatalf("restore after prune: %v", err)
	}
	assertRestored(t, out2, files)

	checkRes, err = openV3Repo(t, storeDir).Check(ctx, WithReadData())
	if err != nil {
		t.Fatalf("check after prune: %v", err)
	}
	if len(checkRes.Errors) != 0 {
		t.Fatalf("check after prune reported %d errors: %v", len(checkRes.Errors), checkRes.Errors)
	}

	// --- Forget the first snapshot, prune, and confirm the survivor ---
	forgetClient := openV3Repo(t, storeDir)
	if _, err := forgetClient.Forget(ctx, res.SnapshotRef); err != nil {
		t.Fatalf("forget: %v", err)
	}
	pruneRes, err := forgetClient.Prune(ctx)
	if err != nil {
		t.Fatalf("prune after forget: %v", err)
	}
	if pruneRes.ObjectsDeleted == 0 {
		t.Error("prune after forget deleted nothing; the first snapshot's garbage survived")
	}

	out3 := t.TempDir()
	if _, err := openV3Repo(t, storeDir).RestoreToDir(ctx, out3, "latest"); err != nil {
		t.Fatalf("restore after forget+prune: %v", err)
	}
	assertRestored(t, out3, files)
	checkRes, err = openV3Repo(t, storeDir).Check(ctx, WithReadData())
	if err != nil {
		t.Fatalf("final check: %v", err)
	}
	if len(checkRes.Errors) != 0 {
		t.Fatalf("final check reported %d errors: %v", len(checkRes.Errors), checkRes.Errors)
	}
}

// TestClientV3_EncryptedRoundTrip covers the v3 chain with encryption on: the
// leaves pass through EncryptedStore like any object, and there is no pack
// index needing its own key.
func TestClientV3_EncryptedRoundTrip(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()

	files := map[string]string{
		"x.txt":       "ex",
		"sub/y.txt":   "why",
		"sub/z/q.txt": "queue",
	}
	writeSourceTree(t, sourceDir, files)

	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, base, WithInitFormat(core.RepoFormatV3)); err != nil {
		t.Fatalf("init v3: %v", err)
	}

	open := func() *Client {
		b, err := localstore.New(storeDir)
		if err != nil {
			t.Fatal(err)
		}
		c, err := NewClient(ctx, b, WithEncryptionKey(packfileTestKey()))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	if _, err := open().Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	for _, prefix := range []string{"filemeta/", "content/", "packs/"} {
		if keys := listPrefix(t, storeDir, prefix); len(keys) != 0 {
			t.Errorf("encrypted v3 repository has objects under %s", prefix)
		}
	}

	out := t.TempDir()
	if _, err := open().RestoreToDir(ctx, out, "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertRestored(t, out, files)
}

// TestClientV3_ChainHasNoPackStore pins the layering claim RFC 0026 makes:
// a v3 client's store chain contains no packfile layer at all.
//
// Asserted on the chain rather than on the objects a run happens to write,
// because those are a consequence. A PackStore present but idle would still
// load a catalog, hold it, and answer reads through it — the residency the
// format exists to remove — and the physical-namespace assertions elsewhere
// would not notice.
func TestClientV3_ChainHasNoPackStore(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()

	// WithPackfile(true) is passed deliberately: the repository's format must
	// win over the caller's preference.
	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, base, WithInitFormat(core.RepoFormatV3)); err != nil {
		t.Fatalf("init v3: %v", err)
	}
	client, err := NewClient(ctx, base, WithPackfile(true))
	if err != nil {
		t.Fatalf("open v3 client: %v", err)
	}

	var layers []string
	for s := client.Store(); s != nil; {
		layers = append(layers, fmt.Sprintf("%T", s))
		if _, isPack := s.(*storelayer.PackStore); isPack {
			t.Fatalf("v3 store chain contains a PackStore: %v", layers)
		}
		un, ok := s.(store.Unwrapper)
		if !ok {
			break
		}
		s = un.Unwrap()
	}
	if len(layers) < 2 {
		t.Fatalf("v3 store chain looks degenerate: %v", layers)
	}
	t.Logf("v3 chain: %v", layers)

	// And the same client on a *packfile* repository still packs, so the
	// absence above is the format's doing and not a broken chain builder.
	// Asked for explicitly: v3 is the default since #517, so "the other
	// format" has to be named rather than assumed.
	v2Dir := t.TempDir()
	v2Base, err := localstore.New(v2Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, v2Base, WithInitFormat(core.RepoFormatV2)); err != nil {
		t.Fatalf("init v2: %v", err)
	}
	v2Client, err := NewClient(ctx, v2Base, WithPackfile(true))
	if err != nil {
		t.Fatalf("open v2 client: %v", err)
	}
	var foundPack bool
	for s := v2Client.Store(); s != nil; {
		if _, isPack := s.(*storelayer.PackStore); isPack {
			foundPack = true
			break
		}
		un, ok := s.(store.Unwrapper)
		if !ok {
			break
		}
		s = un.Unwrap()
	}
	if !foundPack {
		t.Fatal("a v2 client with packfiles enabled has no PackStore")
	}
}

// TestClientV3_RefusesReinitFromV2 pins the migration boundary: a packfile-era
// repository cannot become v3 by rewriting its marker.
func TestClientV3_RefusesReinitFromV2(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	base, err := localstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitRepo(ctx, base, WithInitFormat(core.RepoFormatV2)); err != nil {
		t.Fatalf("init v2: %v", err)
	}
	if _, err := InitRepo(ctx, base, WithInitFormat(core.RepoFormatV3), WithInitAdoptSlots()); err == nil {
		t.Fatal("re-initializing a v2 repository as v3 must be refused")
	}
}
