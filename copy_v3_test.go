package cloudstic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/keychain"
	localsource "github.com/cloudstic/cli/pkg/source/local"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// openRepoFormat initializes a repository at an explicit format and returns a
// client for it. An empty password means an unencrypted repository.
func openRepoFormat(t *testing.T, dir, password string, format int) *Client {
	t.Helper()
	ctx := context.Background()

	backend, err := localstore.New(dir)
	if err != nil {
		t.Fatalf("open store %s: %v", dir, err)
	}

	initOpts := []InitOption{WithInitFormat(format), WithInitNoEncryption()}
	clientOpts := []ClientOption{}
	if password != "" {
		chain := keychain.Chain{keychain.WithPassword(password)}
		initOpts = []InitOption{WithInitFormat(format), WithInitCredentials(chain)}
		clientOpts = append(clientOpts, WithKeychain(chain))
	}
	if _, err := InitRepo(ctx, backend, initOpts...); err != nil {
		t.Fatalf("init %s at format %d: %v", dir, format, err)
	}

	client, err := NewClient(ctx, backend, clientOpts...)
	if err != nil {
		t.Fatalf("open client %s: %v", dir, err)
	}
	return client
}

// writeTree materialises files under a fresh directory and returns its path,
// so the same tree can be backed up into more than one repository.
func writeTree(t *testing.T, files map[string]string) string {
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
	return dir
}

func backupDir(t *testing.T, client *Client, dir string) *BackupResult {
	t.Helper()
	res, err := client.Backup(context.Background(), localsource.New(dir))
	if err != nil {
		t.Fatalf("backup %s: %v", dir, err)
	}
	return res
}

// crossFormatTree exercises every shape an entry can take, because the shapes
// are exactly what differs between the formats: a folder (metadata only), an
// empty file (a content identity with no bytes behind it), a small file (stored
// inline — in a content object in v2, in the leaf in v3), and a file over the
// inline threshold (chunked, so its refs have to be remapped).
func crossFormatTree() map[string]string {
	return map[string]string{
		"small.txt":            "hello cross-format copy",
		"empty.txt":            "",
		"dir/nested/small.txt": "another small file",
		"dir/large.bin":        strings.Repeat("cloudstic cross-format payload\n", 40000), // ~1.2 MiB
	}
}

func copyFormats(t *testing.T, srcFormat, dstFormat int) (src, dst *Client, srcSnap string) {
	t.Helper()
	src = openRepoFormat(t, t.TempDir(), "", srcFormat)
	dst = openRepoFormat(t, t.TempDir(), "", dstFormat)
	res := backupDir(t, src, writeTree(t, crossFormatTree()))
	return src, dst, res.SnapshotRef
}

// TestCopyFrom_CrossesRepositoryFormats is the acceptance case for RFC 0026's
// migration path: all four direction combinations between the packfile-era
// format and v3, each verified the only way that matters — the copied snapshot
// restores to the same bytes, and the destination passes check -read-data.
//
// The namespace assertion is what proves the destination was written in its own
// format rather than the source's. A v3 destination that had been handed v2
// structures would still restore through this build's v2 read paths, and only
// the absence of filemeta/ and content/ objects catches that.
func TestCopyFrom_CrossesRepositoryFormats(t *testing.T) {
	const v2 = core.RepoFormatVersion

	cases := []struct {
		name           string
		srcFmt, dstFmt int
	}{
		{"v2 to v2", v2, v2},
		{"v2 to v3", v2, core.RepoFormatV3},
		{"v3 to v2", core.RepoFormatV3, v2},
		{"v3 to v3", core.RepoFormatV3, core.RepoFormatV3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srcDir, dstDir := t.TempDir(), t.TempDir()
			src := openRepoFormat(t, srcDir, "", tc.srcFmt)
			dst := openRepoFormat(t, dstDir, "", tc.dstFmt)

			srcRes := backupDir(t, src, writeTree(t, crossFormatTree()))

			res, err := dst.CopyFrom(ctx, src)
			if err != nil {
				t.Fatalf("copy: %v", err)
			}
			if len(res.Copied) != 1 {
				t.Fatalf("copied %d snapshots, want 1", len(res.Copied))
			}

			want := restoredFiles(t, src, srcRes.SnapshotRef)
			got := restoredFiles(t, dst, res.Copied[0].DestRef)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("restored tree differs\n source: %v\n   dest: %v", sortedKeys(want), sortedKeys(got))
			}

			check, err := dst.Check(ctx, WithReadData())
			if err != nil {
				t.Fatalf("check destination: %v", err)
			}
			if len(check.Errors) != 0 {
				t.Fatalf("check reported %d errors: %v", len(check.Errors), check.Errors)
			}

			assertNamespaces(t, dstDir, tc.dstFmt)
		})
	}
}

// assertNamespaces asserts that a repository's stored objects are those of the
// format it records, and only those.
func assertNamespaces(t *testing.T, dir string, format int) {
	t.Helper()
	standalone := len(listPrefix(t, dir, "filemeta/")) + len(listPrefix(t, dir, "content/"))
	packs := len(listPrefix(t, dir, "packs/"))

	if format >= core.RepoFormatV3 {
		if standalone != 0 {
			t.Errorf("v3 repository holds %d filemeta/ or content/ objects; entries must live in leaves", standalone)
		}
		if packs != 0 {
			t.Errorf("v3 repository holds %d pack objects; there is no packfile layer", packs)
		}
		return
	}
	// A v2 repository packs its small objects, so the standalone namespaces are
	// empty on disk while the packs are not. Either way it must not be empty of
	// both, which is what a destination written in the wrong format looks like.
	if standalone == 0 && packs == 0 {
		t.Error("v2 repository holds neither packed nor standalone metadata objects")
	}
}

// TestCopyFrom_CrossFormatMatchesADirectBackup is the strong form of "a v3
// repository written by copy is indistinguishable from one written by backup".
//
// It compares what the two repositories *hold*, not their root hashes, and the
// difference is the point. A v3 leaf entry names where its body landed, and
// placement depends on which bodies were packed alongside it — so two runs
// over the same tree produce different roots, whether the runs are a copy and
// a backup or two identical backups. Asserting root equality here was flaky
// for exactly that reason.
//
// What must still hold is everything that is not placement: the same files,
// the same metadata, the same bytes on restore. If copy assembles leaf
// payloads even slightly differently from backup — a size taken from the wrong
// place, a body where a chunk list belongs, a payload omitted on a folder —
// one of those three diverges.
func TestCopyFrom_CrossFormatMatchesADirectBackup(t *testing.T) {
	ctx := context.Background()
	tree := writeTree(t, crossFormatTree())

	v2Repo := openRepoFormat(t, t.TempDir(), "", core.RepoFormatVersion)
	backupDir(t, v2Repo, tree)

	// The same tree, backed up straight into a v3 repository.
	direct := openRepoFormat(t, t.TempDir(), "", core.RepoFormatV3)
	directRes := backupDir(t, direct, tree)

	// ... and reached the other way, by copying the v2 repository into a v3 one.
	copied := openRepoFormat(t, t.TempDir(), "", core.RepoFormatV3)
	res, err := copied.CopyFrom(ctx, v2Repo)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if len(res.Copied) != 1 {
		t.Fatalf("copied %d snapshots, want 1", len(res.Copied))
	}

	// The entry values are content addresses of the metadata, so they are
	// independent of where a body landed. They must agree exactly.
	wantEntries := listEntryValues(t, direct, directRes.SnapshotRef)
	gotEntries := listEntryValues(t, copied, res.Copied[0].DestRef)
	if !reflect.DeepEqual(wantEntries, gotEntries) {
		t.Errorf("entry values differ between a direct v3 backup and a v2->v3 copy:\n backup: %v\n   copy: %v",
			wantEntries, gotEntries)
	}

	// And both restore the tree they came from, byte for byte.
	assertRestoresTo(t, direct, directRes.SnapshotRef, tree)
	assertRestoresTo(t, copied, res.Copied[0].DestRef, tree)
}

// listEntryValues returns every entry in a snapshot's tree as "key=value",
// sorted — the metadata identity of the snapshot, with placement excluded.
func listEntryValues(t *testing.T, client *Client, ref string) []string {
	t.Helper()
	ctx := context.Background()
	root := snapshotRoot(t, client, ref)

	var out []string
	err := hamt.NewTree(client.store, hamt.WithFormatV3()).WalkEntries(ctx, root,
		func(key, value string, _ *hamt.Payload) error {
			out = append(out, key+"="+value)
			return nil
		})
	if err != nil {
		t.Fatalf("walk entries: %v", err)
	}
	sort.Strings(out)
	return out
}

// assertRestoresTo restores a snapshot and compares it with the source tree.
func assertRestoresTo(t *testing.T, client *Client, ref, want string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := client.RestoreToDir(context.Background(), dir, ref); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, wantSum := treeFingerprint(t, dir), treeFingerprint(t, want); got != wantSum {
		t.Errorf("restored tree differs from the source:\n got %s\nwant %s", got, wantSum)
	}
}

func snapshotRoot(t *testing.T, client *Client, ref string) string {
	t.Helper()
	res, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, s := range res.Snapshots {
		if s.Ref == ref {
			return s.Snap.Root
		}
	}
	t.Fatalf("snapshot %s not found", ref)
	return ""
}

// TestCopyFrom_CrossFormatIsIdempotent covers the provenance half of the
// acceptance criteria: a second run must recognise what it already copied, and
// provenance is recorded on the snapshot object, which is JSON in both formats
// — so this is the assertion that the seam did not break the resumability
// migration depends on.
func TestCopyFrom_CrossFormatIsIdempotent(t *testing.T) {
	ctx := context.Background()
	src, dst, _ := copyFormats(t, core.RepoFormatVersion, core.RepoFormatV3)

	first, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("first copy: %v", err)
	}
	if len(first.Copied) != 1 {
		t.Fatalf("first copy moved %d snapshots, want 1", len(first.Copied))
	}

	second, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("second copy: %v", err)
	}
	if len(second.Copied) != 0 {
		t.Fatalf("second copy moved %d snapshots, want 0", len(second.Copied))
	}
	if len(second.Skipped) != 1 {
		t.Fatalf("second copy skipped %d snapshots, want 1", len(second.Skipped))
	}
	if got := len(listRefs(t, dst)); got != 1 {
		t.Fatalf("destination holds %d snapshots after two copies, want 1", got)
	}
}

// TestCopyFrom_CrossFormatPropagatesDeletions exercises the incremental path,
// which is the one that reads a *removed* entry's routing key. In v2 that entry
// is still fetchable as an object; in v3 it exists only as the payload the diff
// hands over, and a copy that ignored it would file the deletion under the wrong
// leaf and leave the deleted file present in the destination.
func TestCopyFrom_CrossFormatPropagatesDeletions(t *testing.T) {
	for _, tc := range []struct {
		name           string
		srcFmt, dstFmt int
	}{
		{"v3 to v2", core.RepoFormatV3, core.RepoFormatVersion},
		{"v2 to v3", core.RepoFormatVersion, core.RepoFormatV3},
		{"v3 to v3", core.RepoFormatV3, core.RepoFormatV3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			src := openRepoFormat(t, t.TempDir(), "", tc.srcFmt)
			dst := openRepoFormat(t, t.TempDir(), "", tc.dstFmt)

			dir := writeTree(t, map[string]string{
				"keep.txt":   "kept across both snapshots",
				"remove.txt": "present only in the first",
			})
			backupDir(t, src, dir)

			if err := os.Remove(filepath.Join(dir, "remove.txt")); err != nil {
				t.Fatalf("remove: %v", err)
			}
			second := backupDir(t, src, dir)

			res, err := dst.CopyFrom(ctx, src)
			if err != nil {
				t.Fatalf("copy: %v", err)
			}
			if len(res.Copied) != 2 {
				t.Fatalf("copied %d snapshots, want 2", len(res.Copied))
			}

			want := restoredFiles(t, src, second.SnapshotRef)
			got := restoredFiles(t, dst, res.Copied[1].DestRef)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("latest snapshot differs\n source: %v\n   dest: %v", sortedKeys(want), sortedKeys(got))
			}
			if _, present := got["remove.txt"]; present {
				t.Error("deleted file survived the copy into the second snapshot")
			}
		})
	}
}

// TestCopyFrom_CrossFormatBetweenEncryptedRepositories keeps the seam honest
// about naming: with different master keys the two repositories name the same
// bytes differently at every level, so a payload carrying a source chunk ref
// through unremapped would produce a destination that cannot be restored.
func TestCopyFrom_CrossFormatBetweenEncryptedRepositories(t *testing.T) {
	ctx := context.Background()
	src := openRepoFormat(t, t.TempDir(), "source-password", core.RepoFormatVersion)
	dst := openRepoFormat(t, t.TempDir(), "destination-password", core.RepoFormatV3)

	srcRes := backupDir(t, src, writeTree(t, crossFormatTree()))

	res, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if len(res.Copied) != 1 {
		t.Fatalf("copied %d snapshots, want 1", len(res.Copied))
	}

	want := restoredFiles(t, src, srcRes.SnapshotRef)
	got := restoredFiles(t, dst, res.Copied[0].DestRef)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("restored tree differs\n source: %v\n   dest: %v", sortedKeys(want), sortedKeys(got))
	}

	check, err := dst.Check(ctx, WithReadData())
	if err != nil {
		t.Fatalf("check destination: %v", err)
	}
	if len(check.Errors) != 0 {
		t.Fatalf("check reported %d errors: %v", len(check.Errors), check.Errors)
	}
}

// treeFingerprint hashes every relative path and its contents, so two trees
// compare equal only if they are byte-identical.
func treeFingerprint(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		h.Write([]byte(rel))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}
