package cloudstic

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/internal/repoconfig"
	localsource "github.com/cloudstic/cli/pkg/source/local"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// Every repository that existed before RepoConfig.ID did has no identifier, and
// so does any whose marker an older build rewrote. `init` is not something an
// operator re-runs on a working repository, so an identifier that only `init`
// could assign would never reach them. These cover the healing path that closes
// that gap, and the two rules it has to respect: mutations only, never fatal.

// markerID reads the identifier out of an unencrypted repository's marker.
func markerID(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse marker: %v", err)
	}
	id, _ := cfg["id"].(string)
	return id
}

// legacyRepo returns a client for a repository whose marker carries no
// identifier, as one written before the field existed does.
func legacyRepo(t *testing.T, dir string) *Client {
	t.Helper()
	openRepo(t, dir, "")
	stripRepoID(t, dir)
	if markerID(t, dir) != "" {
		t.Fatal("failed to strip the identifier from the marker")
	}
	// Reopen so the client's cached view matches what is on disk.
	backend, err := localstore.New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	reopened, err := NewClient(context.Background(), backend)
	if err != nil {
		t.Fatalf("reopen client: %v", err)
	}
	return reopened
}

func TestBackupAssignsAMissingRepositoryID(t *testing.T) {
	dir := t.TempDir()
	client := legacyRepo(t, dir)

	backupTree(t, client, map[string]string{"a.txt": "payload"})

	if markerID(t, dir) == "" {
		t.Error("a repository with no identifier still has none after a backup")
	}
}

func TestPruneAndForgetAssignAMissingRepositoryID(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *Client){
		"prune": func(t *testing.T, c *Client) {
			if _, err := c.Prune(context.Background()); err != nil {
				t.Fatalf("prune: %v", err)
			}
		},
		"forget": func(t *testing.T, c *Client) {
			if _, err := c.ForgetPolicy(context.Background(), WithKeepLast(1)); err != nil {
				t.Fatalf("forget: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			client := openRepo(t, dir, "")
			backupTree(t, client, map[string]string{"a.txt": "payload"})

			stripRepoID(t, dir)
			backend, err := localstore.New(dir)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			reopened, err := NewClient(context.Background(), backend)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}

			mutate(t, reopened)

			if markerID(t, dir) == "" {
				t.Errorf("%s did not assign a missing repository identifier", name)
			}
		})
	}
}

// A read must not rewrite the marker. Doing so would be a surprising side
// effect of listing snapshots, and would fail against the read-only credentials
// that reading a repository is supposed to need.
func TestReadsDoNotAssignARepositoryID(t *testing.T) {
	dir := t.TempDir()
	client := openRepo(t, dir, "")
	backupTree(t, client, map[string]string{"a.txt": "payload"})

	stripRepoID(t, dir)
	backend, err := localstore.New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	reopened, err := NewClient(context.Background(), backend)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	ctx := context.Background()
	if _, err := reopened.List(ctx); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := reopened.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if _, err := reopened.LsSnapshot(ctx, "latest"); err != nil {
		t.Fatalf("ls: %v", err)
	}

	if id := markerID(t, dir); id != "" {
		t.Errorf("a read assigned the repository identifier %q", id)
	}
}

// A dry run wrote nothing and must not start by writing the marker.
func TestDryRunDoesNotAssignARepositoryID(t *testing.T) {
	dir := t.TempDir()
	client := legacyRepo(t, dir)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := client.Backup(context.Background(), localsource.New(src), WithBackupDryRun()); err != nil {
		t.Fatalf("dry-run backup: %v", err)
	}

	if id := markerID(t, dir); id != "" {
		t.Errorf("a dry run assigned the repository identifier %q", id)
	}
}

// An identifier is assigned once. A second mutation must not mint another, or
// provenance recorded under the first would stop matching.
func TestRepositoryIDIsStableAcrossMutations(t *testing.T) {
	dir := t.TempDir()
	client := legacyRepo(t, dir)

	backupTree(t, client, map[string]string{"a.txt": "one"})
	first := markerID(t, dir)
	if first == "" {
		t.Fatal("no identifier was assigned")
	}

	backupTree(t, client, map[string]string{"b.txt": "two"})
	if _, err := client.Prune(context.Background()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if second := markerID(t, dir); second != first {
		t.Errorf("identifier changed across mutations: %q -> %q", first, second)
	}
}

// A repository that already has one must not be rewritten, and the client must
// not fetch the marker to discover that.
func TestExistingRepositoryIDIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	client := openRepo(t, dir, "")
	before := markerID(t, dir)
	if before == "" {
		t.Fatal("init did not assign an identifier")
	}

	backupTree(t, client, map[string]string{"a.txt": "payload"})

	if after := markerID(t, dir); after != before {
		t.Errorf("identifier was rewritten: %q -> %q", before, after)
	}
}

// Assigning an identifier must not change anything else about the repository.
//
// The case that matters is a marker that is plaintext on an encrypted
// repository, which is what builds predating marker sealing left behind.
// Writing it back would seal it, and a sealed marker cannot be opened by those
// builds at all — so an identifier, which only makes `copy` cheaper, would have
// silently withdrawn the repository from every older client that could still
// read it. That trade belongs to the version gate, not here.
func TestAssigningARepositoryIDNeverSealsAPlaintextMarker(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// A repository as a build predating sealing left it: encrypted, but with a
	// plaintext marker, and no identifier.
	base, err := localstore.New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	legacy := RepoConfig{Version: 1, Created: "2026-01-01T00:00:00Z", Encrypted: true}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := base.Put(ctx, "config", raw); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	client, err := NewClient(ctx, base, WithEncryptionKey(make([]byte, 32)))
	if err != nil {
		t.Fatalf("open client: %v", err)
	}
	client.ensureRepoID(ctx)

	after, err := base.Get(ctx, "config")
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if repoconfig.IsSealed(after) {
		t.Error("assigning an identifier sealed a marker that was plaintext, " +
			"withdrawing the repository from builds that predate sealing")
	}
}

// Once healed, copy takes the identifier path rather than probing, and the two
// repositories are correctly told apart.
func TestHealedRepositoryIDIsUsedByCopy(t *testing.T) {
	ctx := context.Background()
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := legacyRepo(t, srcDir)
	dst := legacyRepo(t, dstDir)

	backupTree(t, src, map[string]string{"a.txt": "payload"})
	if markerID(t, srcDir) == "" {
		t.Fatal("the source was not healed by its backup")
	}

	res, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if len(res.Copied) != 1 {
		t.Fatalf("copied %d snapshots, want 1", len(res.Copied))
	}
	if res.SourceRepoID == "" {
		t.Error("the copy did not record the source repository identifier")
	}
	if res.SourceRepoID == res.DestRepoID {
		t.Errorf("both repositories reported the identifier %q", res.SourceRepoID)
	}
}
