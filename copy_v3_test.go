package cloudstic

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
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
// Both repositories end up with a tree over the same files, so if copy assembles
// leaf payloads even slightly differently from backup — a size taken from the
// wrong place, inline bytes where a chunk list belongs, a payload omitted on a
// folder — the encodings differ, and with them every node ref up to the root.
// Comparing the root hash is therefore the whole claim in one assertion.
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

	directRoot := snapshotRoot(t, direct, directRes.SnapshotRef)
	copiedRoot := snapshotRoot(t, copied, res.Copied[0].DestRef)
	if directRoot != copiedRoot {
		t.Fatalf("tree root differs between a direct v3 backup and a v2→v3 copy:\n backup: %s\n   copy: %s",
			directRoot, copiedRoot)
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
