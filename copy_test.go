package cloudstic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/cloudstic/cli/pkg/keychain"
	localsource "github.com/cloudstic/cli/pkg/source/local"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// openRepo initializes a repository under dir and returns a client for it.
// An empty password means an unencrypted repository.
func openRepo(t *testing.T, dir, password string) *Client {
	t.Helper()
	ctx := context.Background()

	backend, err := localstore.New(dir)
	if err != nil {
		t.Fatalf("open store %s: %v", dir, err)
	}

	initOpts := []InitOption{WithInitNoEncryption()}
	clientOpts := []ClientOption{}
	if password != "" {
		chain := keychain.Chain{keychain.WithPassword(password)}
		initOpts = []InitOption{WithInitCredentials(chain)}
		clientOpts = append(clientOpts, WithKeychain(chain))
	}
	if _, err := InitRepo(ctx, backend, initOpts...); err != nil {
		t.Fatalf("init %s: %v", dir, err)
	}

	client, err := NewClient(ctx, backend, clientOpts...)
	if err != nil {
		t.Fatalf("open client %s: %v", dir, err)
	}
	return client
}

// backupTree writes files into a fresh directory and backs it up into client.
func backupTree(t *testing.T, client *Client, files map[string]string) *BackupResult {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	src := localsource.New(dir)
	res, err := client.Backup(context.Background(), src)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	return res
}

// restoredFiles reads every file of a snapshot back out of a repository.
func restoredFiles(t *testing.T, client *Client, snapshotRef string) map[string]string {
	t.Helper()
	out := t.TempDir()
	if _, err := client.RestoreToDir(context.Background(), out, snapshotRef); err != nil {
		t.Fatalf("restore %s: %v", snapshotRef, err)
	}

	files := map[string]string{}
	err := filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(out, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk restored tree: %v", err)
	}
	return files
}

func listRefs(t *testing.T, client *Client) []string {
	t.Helper()
	res, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	refs := make([]string, 0, len(res.Snapshots))
	for _, s := range res.Snapshots {
		refs = append(refs, s.Ref)
	}
	return refs
}

// The case that proves the reference cascade is genuinely rebuilt rather than
// accidentally passed through: two encrypted repositories with *different*
// master keys name the same bytes differently at every level, so a copy that
// moved objects verbatim would produce a destination that cannot be read.
func TestCopyFrom_BetweenEncryptedRepositoriesWithDifferentKeys(t *testing.T) {
	ctx := context.Background()
	src := openRepo(t, t.TempDir(), "source-password")
	dst := openRepo(t, t.TempDir(), "destination-password")

	files := map[string]string{
		"notes.txt":       "hello from the source repository",
		"nested/deep.txt": "a file below a directory",
	}
	backupTree(t, src, files)

	res, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if len(res.Copied) != 1 {
		t.Fatalf("copied %d snapshots, want 1", len(res.Copied))
	}
	if res.BytesRead == 0 {
		t.Error("BytesRead was not accounted for")
	}

	got := restoredFiles(t, dst, res.Copied[0].DestRef)
	for name, want := range files {
		if got[name] != want {
			t.Errorf("restored %q = %q, want %q", name, got[name], want)
		}
	}

	// The destination snapshot is a different object than the source one: it
	// is rebuilt under the destination's key, not relabelled.
	if res.Copied[0].DestRef == res.Copied[0].SourceRef {
		t.Error("destination snapshot kept the source ref, so nothing was re-addressed")
	}
}

func TestCopyFrom_PreservesSnapshotMetadata(t *testing.T) {
	ctx := context.Background()
	src := openRepo(t, t.TempDir(), "source-password")
	dst := openRepo(t, t.TempDir(), "destination-password")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	source := localsource.New(dir)
	if _, err := src.Backup(ctx, source, WithTags("workstation", "nightly")); err != nil {
		t.Fatalf("backup: %v", err)
	}

	srcList, err := src.List(ctx)
	if err != nil {
		t.Fatalf("list source: %v", err)
	}
	original := srcList.Snapshots[0]

	if _, err := dst.CopyFrom(ctx, src); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}

	dstList, err := dst.List(ctx)
	if err != nil {
		t.Fatalf("list destination: %v", err)
	}
	if len(dstList.Snapshots) != 1 {
		t.Fatalf("destination holds %d snapshots, want 1", len(dstList.Snapshots))
	}
	copied := dstList.Snapshots[0]

	// Created is the whole point of the feature: a copy is not a re-backup.
	if copied.Snap.Created != original.Snap.Created {
		t.Errorf("Created = %q, want %q", copied.Snap.Created, original.Snap.Created)
	}
	if len(copied.Snap.Tags) != 2 {
		t.Errorf("Tags = %v, want the source's two tags", copied.Snap.Tags)
	}
	if original.Snap.Source == nil || copied.Snap.Source == nil ||
		copied.Snap.Source.Path != original.Snap.Source.Path {
		t.Errorf("source identity was not preserved: %+v", copied.Snap.Source)
	}
}

// A rerun must skip what it already copied, and must not re-read the source
// data to work that out.
func TestCopyFrom_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	src := openRepo(t, t.TempDir(), "source-password")
	dst := openRepo(t, t.TempDir(), "destination-password")
	backupTree(t, src, map[string]string{"a.txt": "first"})

	first, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("first CopyFrom: %v", err)
	}
	if len(first.Copied) != 1 {
		t.Fatalf("first run copied %d snapshots, want 1", len(first.Copied))
	}

	second, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("second CopyFrom: %v", err)
	}
	if len(second.Copied) != 0 {
		t.Errorf("second run copied %d snapshots, want 0", len(second.Copied))
	}
	if len(second.Skipped) != 1 {
		t.Fatalf("second run skipped %d snapshots, want 1", len(second.Skipped))
	}
	if second.Skipped[0].DestRef != first.Copied[0].DestRef {
		t.Errorf("skip named %q, want the snapshot written first (%q)",
			second.Skipped[0].DestRef, first.Copied[0].DestRef)
	}
	// Reading the source's snapshot data again would defeat the point of
	// recording provenance: the skip decision comes from the destination
	// catalog alone.
	if second.BytesRead != 0 {
		t.Errorf("second run read %d bytes from the source, want 0", second.BytesRead)
	}
	if len(listRefs(t, dst)) != 1 {
		t.Error("rerunning the copy duplicated the snapshot in the destination")
	}
}

// Snapshots after the first in a run are applied as a diff against the previous
// one, so a file that disappeared between two source snapshots has to be
// *removed* from the destination tree rather than merely not re-added. Getting
// this wrong leaves deleted files visible forever in every copied snapshot,
// which no amount of restoring the latest snapshot would reveal.
func TestCopyFrom_PropagatesDeletionsBetweenSnapshots(t *testing.T) {
	ctx := context.Background()
	src := openRepo(t, t.TempDir(), "source-password")
	dst := openRepo(t, t.TempDir(), "destination-password")

	dir := t.TempDir()
	for _, name := range []string{"keep.txt", "doomed.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content of "+name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if _, err := src.Backup(ctx, localsource.New(dir)); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, "doomed.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "added.txt"), []byte("new file"), 0o644); err != nil {
		t.Fatalf("write added.txt: %v", err)
	}
	if _, err := src.Backup(ctx, localsource.New(dir)); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	res, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if len(res.Copied) != 2 {
		t.Fatalf("copied %d snapshots, want 2", len(res.Copied))
	}

	// Copy order is ascending Created, so the first entry is the older snapshot.
	older := restoredFiles(t, dst, res.Copied[0].DestRef)
	newer := restoredFiles(t, dst, res.Copied[1].DestRef)

	if _, ok := older["doomed.txt"]; !ok {
		t.Error("the older copied snapshot lost a file that existed when it was taken")
	}
	if _, ok := newer["doomed.txt"]; ok {
		t.Error("the newer copied snapshot still contains a file deleted before it was taken")
	}
	if newer["added.txt"] != "new file" {
		t.Errorf("added.txt = %q in the newer snapshot, want its content", newer["added.txt"])
	}
	if newer["keep.txt"] != "content of keep.txt" {
		t.Errorf("keep.txt = %q, want it carried through unchanged", newer["keep.txt"])
	}
}

// The incremental path must produce trees that are *semantically* identical to
// the ones a whole-tree rebuild produces: same files, same contents, in every
// copied snapshot.
//
// Semantic rather than byte-for-byte, deliberately. The HAMT is not
// history-independent — identical content reached through different insertion
// histories yields different roots, which is true of `backup` as well — so
// comparing snapshot refs would assert a property the data structure has never
// had and would fail for reasons unrelated to copying.
func TestCopyFrom_IncrementalTreesMatchWholeTreeRebuild(t *testing.T) {
	ctx := context.Background()
	src := openRepo(t, t.TempDir(), "source-password")

	dir := t.TempDir()
	for i := range 6 {
		name := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("payload %d", i)), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if i == 4 {
			// A deletion partway through, so the diff path's Delete branch is
			// part of what is being compared.
			if err := os.Remove(filepath.Join(dir, "f1.txt")); err != nil {
				t.Fatalf("remove: %v", err)
			}
		}
		if _, err := src.Backup(ctx, localsource.New(dir)); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
	}

	// One batched run: the first snapshot takes the whole-tree path and the
	// rest are applied as diffs against their predecessor.
	batched := openRepo(t, t.TempDir(), "destination-password")
	batchedRes, err := batched.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("batched CopyFrom: %v", err)
	}
	if len(batchedRes.Copied) != 6 {
		t.Fatalf("batched copy produced %d snapshots, want 6", len(batchedRes.Copied))
	}

	// One snapshot per run, in the order the batched run used. Each run starts
	// with no previously copied tree for the lineage, so every snapshot takes
	// the whole-tree path. Driving the order from the batched result keeps the
	// pairing exact: several of these snapshots share a Created second, so list
	// order would not be a reliable stand-in.
	individual := openRepo(t, t.TempDir(), "destination-password")
	var oneAtATime []CopiedSnapshot
	for _, want := range batchedRes.Copied {
		res, err := individual.CopyFrom(ctx, src, WithCopySnapshotIDs(want.SourceRef))
		if err != nil {
			t.Fatalf("individual CopyFrom %s: %v", want.SourceRef, err)
		}
		oneAtATime = append(oneAtATime, res.Copied...)
	}

	if len(oneAtATime) != len(batchedRes.Copied) {
		t.Fatalf("one-at-a-time copied %d snapshots, want %d", len(oneAtATime), len(batchedRes.Copied))
	}
	for i := range batchedRes.Copied {
		if batchedRes.Copied[i].SourceRef != oneAtATime[i].SourceRef {
			t.Fatalf("run %d compared different source snapshots", i)
		}
		viaDiff := restoredFiles(t, batched, batchedRes.Copied[i].DestRef)
		viaWhole := restoredFiles(t, individual, oneAtATime[i].DestRef)
		if !reflect.DeepEqual(viaDiff, viaWhole) {
			t.Errorf("snapshot %d: diff path restored %v, whole-tree path restored %v",
				i, sortedKeys(viaDiff), sortedKeys(viaWhole))
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestCopyFrom_DryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	src := openRepo(t, t.TempDir(), "source-password")
	dst := openRepo(t, t.TempDir(), "destination-password")
	backupTree(t, src, map[string]string{"a.txt": "first"})

	res, err := dst.CopyFrom(ctx, src, WithCopyDryRun())
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if !res.DryRun {
		t.Error("result does not record that it was a dry run")
	}
	if len(listRefs(t, dst)) != 0 {
		t.Error("a dry run wrote a snapshot into the destination")
	}
}

// Copying into a repository that already holds the same plaintext must reuse
// what is there. This is the property that makes seeding a shared destination
// affordable, and it holds only because chunk boundaries are reproducible.
func TestCopyFrom_DeduplicatesAgainstExistingDestinationData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("shared payload"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	src := openRepo(t, t.TempDir(), "source-password")
	dst := openRepo(t, t.TempDir(), "destination-password")

	source := localsource.New(dir)
	if _, err := src.Backup(ctx, source, WithGenerator("test")); err != nil {
		t.Fatalf("backup into source: %v", err)
	}
	// The destination backs up the very same directory, so it independently
	// holds every chunk the copy is about to bring over.
	source2 := localsource.New(dir)
	if _, err := dst.Backup(ctx, source2, WithGenerator("test")); err != nil {
		t.Fatalf("backup into destination: %v", err)
	}

	res, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if len(res.Copied) != 1 {
		t.Fatalf("copied %d snapshots, want 1", len(res.Copied))
	}
	if res.BytesWritten > 4096 {
		t.Errorf("copy wrote %d bytes into a destination that already held the data", res.BytesWritten)
	}
}

// stripRepoID rewrites an unencrypted repository's marker to drop its
// identifier, reproducing what an older build leaves behind when it rewrites
// the marker during a format upgrade.
func stripRepoID(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "config")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse marker: %v", err)
	}
	delete(cfg, "id")
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode marker: %v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

// A repository with no identifier still must not be copied into itself. The
// identifier is the exact test, but it is absent on repositories written before
// it existed and on any whose marker an older build has rewritten — and a
// self-copy doubles the history rather than failing loudly.
func TestCopyFrom_RefusesTheSameRepositoryWithoutAnID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	client := openRepo(t, dir, "")
	backupTree(t, client, map[string]string{"a.txt": "payload"})
	stripRepoID(t, dir)

	before := len(listRefs(t, client))

	backend, err := localstore.New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	twin, err := NewClient(ctx, backend)
	if err != nil {
		t.Fatalf("open twin client: %v", err)
	}
	// Reopen the destination too, so neither guard can succeed by pointer
	// identity alone.
	destBackend, err := localstore.New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	dest, err := NewClient(ctx, destBackend)
	if err != nil {
		t.Fatalf("open destination client: %v", err)
	}

	if _, err := dest.CopyFrom(ctx, twin); err == nil {
		t.Fatal("copying an id-less repository into itself was allowed")
	}
	if after := len(listRefs(t, client)); after != before {
		t.Errorf("history changed from %d to %d snapshots", before, after)
	}
}

// The fallback must not refuse legitimate work: two distinct repositories that
// both lack an identifier are still two repositories.
func TestCopyFrom_AllowsDistinctRepositoriesWithoutIDs(t *testing.T) {
	ctx := context.Background()
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := openRepo(t, srcDir, "")
	dst := openRepo(t, dstDir, "")
	backupTree(t, src, map[string]string{"a.txt": "payload"})
	stripRepoID(t, srcDir)
	stripRepoID(t, dstDir)

	res, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("CopyFrom between two id-less repositories: %v", err)
	}
	if len(res.Copied) != 1 {
		t.Errorf("copied %d snapshots, want 1", len(res.Copied))
	}
}

func TestCopyFrom_RefusesTheSameRepository(t *testing.T) {
	dir := t.TempDir()
	client := openRepo(t, dir, "password")

	backend, err := localstore.New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	twin, err := NewClient(context.Background(), backend,
		WithKeychain(keychain.Chain{keychain.WithPassword("password")}))
	if err != nil {
		t.Fatalf("open twin client: %v", err)
	}

	_, err = client.CopyFrom(context.Background(), twin)
	if err == nil {
		t.Fatal("copying a repository into itself was allowed")
	}
}
