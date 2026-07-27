package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
)

// ---------------------------------------------------------------------------
// Test repository builder
// ---------------------------------------------------------------------------

// findRepo builds small repositories directly, one snapshot at a time, so a
// test can state exactly which metadata objects each snapshot holds without
// going through a backup.
type findRepo struct {
	t     *testing.T
	store *MockStore
	tree  *hamt.Tree
	seq   int
}

func newFindRepo(t *testing.T) *findRepo {
	t.Helper()
	s := NewMockStore()
	return &findRepo{t: t, store: s, tree: hamt.NewTree(s)}
}

// entry is one file or folder as a snapshot should hold it.
type entry struct {
	id      string
	name    string
	parents []string
	ftype   core.FileType
	size    int64
	mtime   int64
	content string
	paths   []string // legacy persisted paths; normally empty (RFC 0015)
}

func file(id, name string, parents ...string) entry {
	return entry{id: id, name: name, parents: parents, ftype: core.FileTypeFile, size: 100, mtime: 1_700_000_000}
}

func folder(id, name string, parents ...string) entry {
	return entry{id: id, name: name, parents: parents, ftype: core.FileTypeFolder}
}

func (e entry) withSize(n int64) entry     { e.size = n; return e }
func (e entry) withMtime(t int64) entry    { e.mtime = t; return e }
func (e entry) withContent(c string) entry { e.content = c; return e }
func (e entry) withPaths(p ...string) entry {
	e.paths = p
	return e
}

func (e entry) meta() core.FileMeta {
	contentHash := e.content
	if contentHash == "" && e.ftype == core.FileTypeFile {
		contentHash = "hash-" + e.id
	}
	return core.FileMeta{
		Version:     1,
		FileID:      e.id,
		Name:        e.name,
		Type:        e.ftype,
		Parents:     e.parents,
		Paths:       e.paths,
		ContentHash: contentHash,
		Size:        e.size,
		Mtime:       e.mtime,
	}
}

// snapshot writes every entry's metadata, builds a HAMT holding them, and
// records a snapshot over it.
func (r *findRepo) snapshot(created string, source *core.SourceInfo, entries ...entry) string {
	r.t.Helper()
	ctx := context.Background()

	root := ""
	for _, e := range entries {
		meta := e.meta()
		ref, data, err := meta.Ref()
		if err != nil {
			r.t.Fatalf("compute filemeta ref: %v", err)
		}
		if err := r.store.Put(ctx, ref, data); err != nil {
			r.t.Fatalf("put filemeta: %v", err)
		}
		parentID := ""
		if len(e.parents) > 0 {
			parentID = e.parents[0]
		}
		if root, err = insertCommit(ctx, r.tree, root, parentID, e.id, ref); err != nil {
			r.t.Fatalf("insert %s: %v", e.id, err)
		}
	}

	r.seq++
	snap := core.Snapshot{Version: 1, Created: created, Root: root, Seq: r.seq, Source: source}
	hash, data, err := core.ComputeJSONHash(snap)
	if err != nil {
		r.t.Fatalf("hash snapshot: %v", err)
	}
	ref := "snapshot/" + hash
	if err := r.store.Put(ctx, ref, data); err != nil {
		r.t.Fatalf("put snapshot: %v", err)
	}
	idx, _ := json.Marshal(core.Index{LatestSnapshot: ref, Seq: r.seq})
	if err := r.store.Put(ctx, "index/latest", idx); err != nil {
		r.t.Fatalf("put index/latest: %v", err)
	}
	return ref
}

func localSource(path string) *core.SourceInfo {
	return &core.SourceInfo{Type: "local", Account: "host", Path: path, Identity: "vol-1", PathID: path}
}

// runFind executes a query against the repository under both scanners and
// requires them to agree, so every test in this file doubles as a check that
// the delta scan matches the straightforward walk.
func (r *findRepo) runFind(opts ...FindOption) *FindResult {
	r.t.Helper()
	ctx := context.Background()

	delta, err := NewFindManager(r.store).Run(ctx, opts...)
	if err != nil {
		r.t.Fatalf("find (delta): %v", err)
	}
	full, err := NewFindManager(r.store).Run(ctx, append(append([]FindOption{}, opts...), WithFindNoDelta())...)
	if err != nil {
		r.t.Fatalf("find (no-delta): %v", err)
	}
	assertSameMatches(r.t, delta, full)
	return delta
}

func assertSameMatches(t *testing.T, delta, full *FindResult) {
	t.Helper()
	if delta.Truncated != full.Truncated {
		t.Errorf("truncation differs: delta=%v full=%v", delta.Truncated, full.Truncated)
	}
	if delta.Truncated {
		// A truncated result is the first N matches each scanner happened to
		// encounter, and the two visit entries in different orders. Comparing
		// identity there would be testing an arbitrary sample, not the model.
		if len(delta.Matches) != len(full.Matches) {
			t.Errorf("truncated match counts differ: delta=%d full=%d", len(delta.Matches), len(full.Matches))
		}
		return
	}
	if got, want := renderMatches(delta.Matches), renderMatches(full.Matches); got != want {
		t.Errorf("delta scan and full scan disagree\ndelta:\n%s\nfull:\n%s", got, want)
	}
}

// renderMatches flattens a result into a comparable, human-readable form.
func renderMatches(matches []FileMatch) string {
	var b strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&b, "match id=%s content=%s type=%s\n", m.FileID, m.ContentHash, m.Type)
		for _, v := range m.Versions {
			var snaps []string
			for _, s := range v.Snapshots {
				snaps = append(snaps, s.Created)
			}
			fmt.Fprintf(&b, "  version ref=%s name=%s paths=%v size=%d snapshots=%v\n",
				v.Ref, v.Name, v.Paths, v.Size, snaps)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Axis 1: the same file, unchanged, across many snapshots
// ---------------------------------------------------------------------------

func TestFind_UnchangedFileCollapsesToOneVersion(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	docs := folder("d1", "Documents")
	vault := file("f1", "vault.kdbx", "d1")

	for day := 1; day <= 5; day++ {
		r.snapshot(fmt.Sprintf("2026-01-0%dT00:00:00Z", day), src, docs, vault)
	}

	result := r.runFind(WithFindPattern("vault.kdbx"))

	if len(result.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %s", len(result.Matches), renderMatches(result.Matches))
	}
	m := result.Matches[0]
	if len(m.Versions) != 1 {
		t.Fatalf("an unchanged file must collapse to one version, got %d", len(m.Versions))
	}
	if got := len(m.Versions[0].Snapshots); got != 5 {
		t.Errorf("want the single version credited with 5 snapshots, got %d", got)
	}
	if got := m.Versions[0].Paths; len(got) != 1 || got[0] != "Documents/vault.kdbx" {
		t.Errorf("paths = %v, want [Documents/vault.kdbx]", got)
	}
	if m.Versions[0].FirstSeen != "2026-01-01T00:00:00Z" || m.Versions[0].LastSeen != "2026-01-05T00:00:00Z" {
		t.Errorf("first/last seen = %s/%s", m.Versions[0].FirstSeen, m.Versions[0].LastSeen)
	}
}

// ---------------------------------------------------------------------------
// Axis 2: the same file, edited, across snapshots
// ---------------------------------------------------------------------------

func TestFind_EditedFileYieldsOrderedVersions(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	docs := folder("d1", "Documents")
	vault := file("f1", "vault.kdbx", "d1")

	r.snapshot("2026-01-01T00:00:00Z", src, docs, vault.withSize(100))
	r.snapshot("2026-01-02T00:00:00Z", src, docs, vault.withSize(100))
	r.snapshot("2026-01-03T00:00:00Z", src, docs, vault.withSize(200).withContent("v2"))
	r.snapshot("2026-01-04T00:00:00Z", src, docs, vault.withSize(300).withContent("v3"))

	result := r.runFind(WithFindPattern("vault.kdbx"))

	if len(result.Matches) != 1 {
		t.Fatalf("an edited file stays one match, got %d: %s", len(result.Matches), renderMatches(result.Matches))
	}
	versions := result.Matches[0].Versions
	if len(versions) != 3 {
		t.Fatalf("want 3 versions, got %d: %s", len(versions), renderMatches(result.Matches))
	}
	// Newest first.
	wantSizes := []int64{300, 200, 100}
	for i, want := range wantSizes {
		if versions[i].Size != want {
			t.Errorf("version %d size = %d, want %d", i, versions[i].Size, want)
		}
	}
	if got := len(versions[2].Snapshots); got != 2 {
		t.Errorf("the oldest version spans 2 snapshots, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Axis 3: several copies of the same content inside one snapshot
// ---------------------------------------------------------------------------

func TestFind_IdenticalContentAtDifferentPathsStaysSeparate(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	r.snapshot("2026-01-01T00:00:00Z", src,
		folder("d1", "Documents"),
		folder("d2", "Backup"),
		file("f1", "report.pdf", "d1").withContent("same-bytes"),
		file("f2", "report.pdf", "d2").withContent("same-bytes"),
	)

	result := r.runFind(WithFindPattern("report.pdf"))
	if len(result.Matches) != 2 {
		t.Fatalf("two distinct files must stay separate matches, got %d: %s",
			len(result.Matches), renderMatches(result.Matches))
	}
	if a, b := result.Matches[0].FileID, result.Matches[1].FileID; a == b {
		t.Errorf("matches share a FileID %q", a)
	}

	grouped := r.runFind(WithFindPattern("report.pdf"), WithFindGroupByContent())
	if len(grouped.Matches) != 1 {
		t.Fatalf("-by-content must group them into one, got %d: %s",
			len(grouped.Matches), renderMatches(grouped.Matches))
	}
	if got := len(grouped.Matches[0].Versions); got != 2 {
		t.Errorf("the content group holds both files' versions, got %d", got)
	}
	if grouped.Matches[0].ContentHash != "same-bytes" {
		t.Errorf("content group hash = %q", grouped.Matches[0].ContentHash)
	}
	if grouped.GroupedBy != "content" {
		t.Errorf("GroupedBy = %q, want content", grouped.GroupedBy)
	}
}

// ---------------------------------------------------------------------------
// Axis 4: one file reachable by several paths
// ---------------------------------------------------------------------------

func TestFind_MultiParentEntryReportsEveryPath(t *testing.T) {
	r := newFindRepo(t)
	src := &core.SourceInfo{Type: "gdrive", Account: "me@example.com", Identity: "drive-1"}
	r.snapshot("2026-01-01T00:00:00Z", src,
		folder("d1", "Work"),
		folder("d2", "Shared"),
		file("f1", "spec.md", "d1", "d2"),
	)

	result := r.runFind(WithFindPattern("spec.md"))
	if len(result.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(result.Matches))
	}
	paths := result.Matches[0].Versions[0].Paths
	if len(paths) != 2 {
		t.Fatalf("want both paths, got %v", paths)
	}
	if paths[0] != "Work/spec.md" || paths[1] != "Shared/spec.md" {
		t.Errorf("paths = %v, want [Work/spec.md Shared/spec.md]", paths)
	}

	// Either path is a valid way to ask for it.
	for _, pattern := range []string{"Work/spec.md", "Shared/spec.md"} {
		got := r.runFind(WithFindPattern(pattern))
		if len(got.Matches) != 1 {
			t.Errorf("pattern %q matched %d entries, want 1", pattern, len(got.Matches))
		}
	}
}

// ---------------------------------------------------------------------------
// Renames
// ---------------------------------------------------------------------------

func TestFind_RenameIsOneMatchWithDifferingVersionNames(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	docs := folder("d1", "Documents")

	r.snapshot("2026-01-01T00:00:00Z", src, docs, file("f1", "old-name.txt", "d1"))
	r.snapshot("2026-01-02T00:00:00Z", src, docs, file("f1", "new-name.txt", "d1"))

	byID := r.runFind(WithFindFileID("f1"))
	if len(byID.Matches) != 1 {
		t.Fatalf("a renamed file is one match, got %d: %s", len(byID.Matches), renderMatches(byID.Matches))
	}
	if got := len(byID.Matches[0].Versions); got != 2 {
		t.Fatalf("want 2 versions across the rename, got %d", got)
	}
	names := []string{byID.Matches[0].Versions[0].Name, byID.Matches[0].Versions[1].Name}
	if names[0] != "new-name.txt" || names[1] != "old-name.txt" {
		t.Errorf("version names = %v, want [new-name.txt old-name.txt]", names)
	}

	// A name query matches only the versions bearing that name. Pulling in the
	// post-rename versions would make the result set depend on grouping.
	byName := r.runFind(WithFindPattern("old-name.txt"))
	if len(byName.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(byName.Matches))
	}
	if got := len(byName.Matches[0].Versions); got != 1 {
		t.Fatalf("a name query must not pull in the renamed version, got %d versions", got)
	}
	if byName.Matches[0].Versions[0].Name != "old-name.txt" {
		t.Errorf("matched version name = %q", byName.Matches[0].Versions[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Deletion and re-addition
// ---------------------------------------------------------------------------

func TestFind_DeletedAndReaddedFileHasNonContiguousSnapshots(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	docs := folder("d1", "Documents")
	notes := file("f1", "notes.txt", "d1")

	first := r.snapshot("2026-01-01T00:00:00Z", src, docs, notes)
	r.snapshot("2026-01-02T00:00:00Z", src, docs) // deleted
	third := r.snapshot("2026-01-03T00:00:00Z", src, docs, notes)

	result := r.runFind(WithFindPattern("notes.txt"))
	if len(result.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(result.Matches))
	}
	versions := result.Matches[0].Versions
	if len(versions) != 1 {
		t.Fatalf("the same bytes re-added is one version, got %d", len(versions))
	}
	snaps := versions[0].Snapshots
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(snaps))
	}
	got := map[string]bool{snaps[0].Ref: true, snaps[1].Ref: true}
	if !got[first] || !got[third] {
		t.Errorf("want the first and third snapshots, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Legacy persisted paths
// ---------------------------------------------------------------------------

func TestFind_LegacyStoredPathIsHonored(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	// No parent is present in the tree at all; only the persisted path says
	// where this entry lived. Snapshots predating RFC 0015 look like this.
	r.snapshot("2026-01-01T00:00:00Z", src,
		file("f1", "legacy.txt", "missing-parent").withPaths("Archive/2019/legacy.txt"),
	)

	result := r.runFind(WithFindPath("Archive/**/legacy.txt"))
	if len(result.Matches) != 1 {
		t.Fatalf("a stored path must be usable for matching, got %d matches", len(result.Matches))
	}
	if got := result.Matches[0].Versions[0].Paths[0]; got != "Archive/2019/legacy.txt" {
		t.Errorf("path = %q, want the stored path", got)
	}
}

// ---------------------------------------------------------------------------
// Ancestor rename: same ref, different path
// ---------------------------------------------------------------------------

func TestFind_AncestorRenameSplitsVersionsByPath(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	notes := file("f1", "notes.txt", "d1")

	r.snapshot("2026-01-01T00:00:00Z", src, folder("d1", "Documents"), notes)
	r.snapshot("2026-01-02T00:00:00Z", src, folder("d1", "Papers"), notes)

	result := r.runFind(WithFindPattern("notes.txt"))
	if len(result.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(result.Matches))
	}
	versions := result.Matches[0].Versions
	if len(versions) != 2 {
		t.Fatalf("renaming an ancestor changes the path, so the file is reported at each: got %d versions:\n%s",
			len(versions), renderMatches(result.Matches))
	}
	if versions[0].Ref != versions[1].Ref {
		t.Errorf("the file's own metadata object did not change, so both versions share a ref: %s vs %s",
			versions[0].Ref, versions[1].Ref)
	}
	if versions[0].Paths[0] != "Papers/notes.txt" || versions[1].Paths[0] != "Documents/notes.txt" {
		t.Errorf("paths = %q then %q", versions[0].Paths[0], versions[1].Paths[0])
	}

	// A path query sees only the snapshots where the file was really there.
	old := r.runFind(WithFindPath("Documents/notes.txt"))
	if len(old.Matches) != 1 || len(old.Matches[0].Versions[0].Snapshots) != 1 {
		t.Errorf("the old path must match only the first snapshot: %s", renderMatches(old.Matches))
	}
}

// ---------------------------------------------------------------------------
// Predicates
// ---------------------------------------------------------------------------

func TestFind_Predicates(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	entries := []entry{
		folder("d1", "Documents"),
		folder("d2", "2026", "d1"),
		file("f1", "report.pdf", "d1").withSize(5 << 20).withMtime(1_600_000_000),
		file("f2", "notes.txt", "d2").withSize(1 << 10).withMtime(1_800_000_000),
		file("f3", "report.pdf", "d2").withSize(20 << 20).withContent("shared"),
	}
	r.snapshot("2026-01-01T00:00:00Z", src, entries...)

	cases := []struct {
		name  string
		opts  []FindOption
		paths []string
	}{
		{"name glob", []FindOption{WithFindPattern("*.pdf")},
			[]string{"Documents/2026/report.pdf", "Documents/report.pdf"}},
		{"path glob", []FindOption{WithFindPattern("Documents/*.pdf")},
			[]string{"Documents/report.pdf"}},
		{"double star", []FindOption{WithFindPattern("Documents/**/report.pdf")},
			[]string{"Documents/2026/report.pdf", "Documents/report.pdf"}},
		{"regex", []FindOption{WithFindRegex(`2026/.*\.pdf$`)},
			[]string{"Documents/2026/report.pdf"}},
		{"case insensitive", []FindOption{WithFindName("REPORT.PDF"), WithFindIgnoreCase()},
			[]string{"Documents/2026/report.pdf", "Documents/report.pdf"}},
		{"type folder", []FindOption{WithFindType(core.FileTypeFolder)},
			[]string{"Documents", "Documents/2026"}},
		{"size at least", []FindOption{WithFindName("*"), WithFindSize(SizeCompare{Op: SizeAtLeast, Bytes: 10 << 20})},
			[]string{"Documents/2026/report.pdf"}},
		{"size at most", []FindOption{WithFindName("*.txt"), WithFindSize(SizeCompare{Op: SizeAtMost, Bytes: 2 << 10})},
			[]string{"Documents/2026/notes.txt"}},
		{"content hash", []FindOption{WithFindContentHash("shared")},
			[]string{"Documents/2026/report.pdf"}},
		{"file id", []FindOption{WithFindFileID("f2")},
			[]string{"Documents/2026/notes.txt"}},
		{"conjunction", []FindOption{WithFindPattern("*.pdf"), WithFindSize(SizeCompare{Op: SizeAtMost, Bytes: 10 << 20})},
			[]string{"Documents/report.pdf"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := r.runFind(tc.opts...)
			var got []string
			for _, m := range result.Matches {
				got = append(got, m.Path())
			}
			if strings.Join(got, ",") != strings.Join(tc.paths, ",") {
				t.Errorf("paths = %v, want %v", got, tc.paths)
			}
		})
	}
}

func TestFind_RefPredicateMatchesExactlyOneMetadataObject(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	target := file("f1", "report.pdf", "d1")
	r.snapshot("2026-01-01T00:00:00Z", src, folder("d1", "Documents"), target, file("f2", "other.pdf", "d1"))

	meta := target.meta()
	ref, _, err := meta.Ref()
	if err != nil {
		t.Fatalf("ref: %v", err)
	}
	result := r.runFind(WithFindRef(ref))
	if len(result.Matches) != 1 || result.Matches[0].Versions[0].Ref != ref {
		t.Fatalf("want exactly the named object, got %s", renderMatches(result.Matches))
	}

	// A bare hash is accepted as well.
	bare := r.runFind(WithFindRef(strings.TrimPrefix(ref, "filemeta/")))
	if len(bare.Matches) != 1 {
		t.Errorf("a bare hash must resolve to the same object, got %d matches", len(bare.Matches))
	}
}

func TestFind_MtimePredicates(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.snapshot("2026-02-01T00:00:00Z", src,
		folder("d1", "Documents"),
		file("f1", "old.txt", "d1").withMtime(old.Unix()),
		file("f2", "recent.txt", "d1").withMtime(recent.Unix()),
	)

	newer := r.runFind(WithFindName("*.txt"), WithFindNewer("2023-01-01"))
	if len(newer.Matches) != 1 || newer.Matches[0].Versions[0].Name != "recent.txt" {
		t.Errorf("-newer selected %s", renderMatches(newer.Matches))
	}
	older := r.runFind(WithFindName("*.txt"), WithFindOlder("2023-01-01"))
	if len(older.Matches) != 1 || older.Matches[0].Versions[0].Name != "old.txt" {
		t.Errorf("-older selected %s", renderMatches(older.Matches))
	}
}

// ---------------------------------------------------------------------------
// Snapshot selection
// ---------------------------------------------------------------------------

func TestFind_SnapshotSelectors(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	docs := folder("d1", "Documents")

	first := r.snapshot("2026-01-01T00:00:00Z", src, docs, file("f1", "notes.txt", "d1").withSize(10))
	r.snapshot("2026-02-01T00:00:00Z", src, docs, file("f1", "notes.txt", "d1").withSize(20))
	r.snapshot("2026-03-01T00:00:00Z", src, docs, file("f1", "notes.txt", "d1").withSize(30))

	t.Run("explicit snapshot", func(t *testing.T) {
		result := r.runFind(WithFindPattern("notes.txt"), WithFindSnapshots(first))
		if result.SnapshotsSearched != 1 {
			t.Fatalf("searched %d snapshots, want 1", result.SnapshotsSearched)
		}
		if got := result.Matches[0].Versions[0].Size; got != 10 {
			t.Errorf("size = %d, want the version in the named snapshot", got)
		}
	})

	t.Run("hash prefix", func(t *testing.T) {
		short := strings.TrimPrefix(first, "snapshot/")[:8]
		result := r.runFind(WithFindPattern("notes.txt"), WithFindSnapshots(short))
		if result.SnapshotsSearched != 1 {
			t.Errorf("a hash prefix must resolve to one snapshot, searched %d", result.SnapshotsSearched)
		}
	})

	t.Run("latest", func(t *testing.T) {
		result := r.runFind(WithFindPattern("notes.txt"), WithFindSnapshots("latest"))
		if got := result.Matches[0].Versions[0].Size; got != 30 {
			t.Errorf("size = %d, want the newest version", got)
		}
	})

	t.Run("latest n", func(t *testing.T) {
		result := r.runFind(WithFindPattern("notes.txt"), WithFindLatest(2))
		if result.SnapshotsSearched != 2 {
			t.Errorf("searched %d snapshots, want 2", result.SnapshotsSearched)
		}
	})

	t.Run("since selects snapshots not files", func(t *testing.T) {
		result := r.runFind(WithFindPattern("notes.txt"), WithFindSince("2026-02-15"))
		if result.SnapshotsSearched != 1 {
			t.Errorf("searched %d snapshots, want 1", result.SnapshotsSearched)
		}
	})

	t.Run("unknown snapshot is an error", func(t *testing.T) {
		_, err := NewFindManager(r.store).Run(context.Background(),
			WithFindPattern("notes.txt"), WithFindSnapshots("deadbeef"))
		if err == nil {
			t.Fatal("want an error for an unknown snapshot")
		}
		if !errors.Is(err, ErrSnapshotNotFound) {
			t.Fatalf("error = %v, want errors.Is(err, ErrSnapshotNotFound)", err)
		}
	})
}

func TestFind_SeparateLineagesAreScannedSeparately(t *testing.T) {
	r := newFindRepo(t)
	laptop := localSource("./laptop")
	desktop := &core.SourceInfo{Type: "local", Account: "other", Path: "./desktop", Identity: "vol-2", PathID: "./desktop"}

	r.snapshot("2026-01-01T00:00:00Z", laptop, folder("d1", "Documents"), file("f1", "notes.txt", "d1"))
	r.snapshot("2026-01-02T00:00:00Z", desktop, folder("d2", "Documents"), file("f2", "notes.txt", "d2"))

	all := r.runFind(WithFindPattern("notes.txt"))
	if len(all.Matches) != 2 {
		t.Fatalf("two sources hold distinct files, want 2 matches, got %d: %s",
			len(all.Matches), renderMatches(all.Matches))
	}

	filtered := r.runFind(WithFindPattern("notes.txt"), WithFindSource("local:./laptop"))
	if len(filtered.Matches) != 1 || filtered.Matches[0].FileID != "f1" {
		t.Errorf("-source must narrow to one lineage: %s", renderMatches(filtered.Matches))
	}
}

// ---------------------------------------------------------------------------
// Bounding and errors
// ---------------------------------------------------------------------------

func TestFind_MaxResultsTruncatesButKeepsCountersAccurate(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	entries := []entry{folder("d1", "Documents")}
	for i := range 10 {
		entries = append(entries, file(fmt.Sprintf("f%d", i), fmt.Sprintf("file%d.txt", i), "d1"))
	}
	r.snapshot("2026-01-01T00:00:00Z", src, entries...)

	result := r.runFind(WithFindPattern("*.txt"), WithFindMaxResults(3))
	if len(result.Matches) != 3 {
		t.Fatalf("want the cap honored, got %d matches", len(result.Matches))
	}
	if !result.Truncated {
		t.Error("Truncated must be set when the cap bites")
	}
	if result.EntriesScanned != 11 {
		t.Errorf("EntriesScanned = %d, want every entry counted despite truncation", result.EntriesScanned)
	}
}

func TestFind_EmptyQueryIsRejected(t *testing.T) {
	r := newFindRepo(t)
	if _, err := NewFindManager(r.store).Run(context.Background()); err == nil {
		t.Fatal("a query with no predicate must be refused rather than dumping the repository")
	}
}

func TestFind_InvalidPatternsFailBeforeScanning(t *testing.T) {
	r := newFindRepo(t)
	for _, opt := range []FindOption{
		WithFindName("[unterminated"),
		WithFindRegex("("),
		WithFindType("symlink"),
		WithFindNewer("not-a-time"),
	} {
		if _, err := NewFindManager(r.store).Run(context.Background(), opt); err == nil {
			t.Error("want a compile error before the scan starts")
		}
	}
}

func TestFind_NoMatchesIsNotAnError(t *testing.T) {
	r := newFindRepo(t)
	r.snapshot("2026-01-01T00:00:00Z", localSource("./docs"), folder("d1", "Documents"))

	result := r.runFind(WithFindPattern("nothing-here.txt"))
	if len(result.Matches) != 0 {
		t.Errorf("want no matches, got %d", len(result.Matches))
	}
	if result.SnapshotsSearched != 1 {
		t.Errorf("SnapshotsSearched = %d, want 1", result.SnapshotsSearched)
	}
}

func TestFind_MissingMetadataObjectIsAnError(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")
	r.snapshot("2026-01-01T00:00:00Z", src, folder("d1", "Documents"), file("f1", "notes.txt", "d1"))

	// A snapshot referencing metadata the store no longer holds is corruption.
	// Reporting "no matches" would be indistinguishable from a real absence.
	for key := range r.store.Data {
		if strings.HasPrefix(key, "filemeta/") {
			delete(r.store.Data, key)
			break
		}
	}
	if _, err := NewFindManager(r.store).Run(context.Background(), WithFindPattern("*.txt")); err == nil {
		t.Fatal("want an error when a referenced filemeta cannot be read")
	}
}

// ---------------------------------------------------------------------------
// Delta scan economics
// ---------------------------------------------------------------------------

// TestFind_DeltaScanReadsFarFewerObjectsThanFullScan is the guard on the whole
// point of the delta scan. It still returns correct results if it degrades to a
// per-snapshot walk, so nothing else in the suite would notice.
func TestFind_DeltaScanReadsFarFewerObjectsThanFullScan(t *testing.T) {
	r := newFindRepo(t)
	src := localSource("./docs")

	entries := []entry{folder("d1", "Documents")}
	for i := range 50 {
		entries = append(entries, file(fmt.Sprintf("f%d", i), fmt.Sprintf("file%d.txt", i), "d1"))
	}
	const snapshots = 20
	for day := range snapshots {
		// One file changes per snapshot; everything else is untouched.
		churned := make([]entry, len(entries))
		copy(churned, entries)
		churned[1] = churned[1].withSize(int64(day + 1))
		r.snapshot(time.Date(2026, 1, day+1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), src, churned...)
	}

	ctx := context.Background()
	delta, err := NewFindManager(r.store).Run(ctx, WithFindPattern("*.txt"))
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	full, err := NewFindManager(r.store).Run(ctx, WithFindPattern("*.txt"), WithFindNoDelta())
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	assertSameMatches(t, delta, full)

	// The delta scan should cost roughly one walk plus the churn, not one walk
	// per snapshot. Both scanners memoize per ref, so the comparison that
	// actually distinguishes them is entries visited.
	if delta.EntriesScanned >= full.EntriesScanned/4 {
		t.Errorf("delta scan visited %d entries against the full scan's %d: the structural sharing is not being exploited",
			delta.EntriesScanned, full.EntriesScanned)
	}
}
