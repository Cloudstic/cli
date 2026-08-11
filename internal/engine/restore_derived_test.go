package engine

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
)

// handBuiltSnapshot assembles a snapshot from entries whose routing keys the
// test chooses, so a traversal that assumes affinity routing can be shown what
// it does when it does not hold.
type handBuiltSnapshot struct {
	store *MockStore
	tree  *hamt.Tree
	root  string
}

func newHandBuiltSnapshot(t *testing.T) *handBuiltSnapshot {
	t.Helper()
	s := NewMockStore()
	return &handBuiltSnapshot{store: s, tree: hamt.NewTree(s)}
}

// add writes an entry's filemeta (and content, for a file) and routes it under
// routingKey — which the caller supplies so that a pre-affinity layout can be
// reproduced exactly.
func (h *handBuiltSnapshot) add(t *testing.T, meta core.FileMeta, body string, routingKey string) {
	t.Helper()
	ctx := context.Background()

	if meta.Type != core.FileTypeFolder {
		content := core.Content{Type: core.ObjectTypeContent, Size: int64(len(body)), DataInlineB64: []byte(body)}
		hash, data, err := core.ComputeJSONHash(&content)
		if err != nil {
			t.Fatalf("content ref: %v", err)
		}
		if err := h.store.Put(ctx, "content/"+hash, data); err != nil {
			t.Fatalf("put content: %v", err)
		}
		meta.Size = int64(len(body))
		meta.ContentRef = hash
		meta.ContentHash = core.ComputeHash([]byte(body))
	}

	ref, data, err := core.FileMetaRef(&meta)
	if err != nil {
		t.Fatalf("filemeta ref: %v", err)
	}
	if err := h.store.Put(ctx, ref, data); err != nil {
		t.Fatalf("put filemeta: %v", err)
	}

	txn := h.tree.Edit(h.root)
	if err := txn.Insert(ctx, routingKey, meta.FileID, ref); err != nil {
		t.Fatalf("insert %s: %v", meta.FileID, err)
	}
	if h.root, err = txn.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func (h *handBuiltSnapshot) seal(t *testing.T) *MockStore {
	t.Helper()
	ctx := context.Background()
	snap := core.Snapshot{Root: h.root, Seq: 1, Created: "2026-08-11T00:00:00Z"}
	hash, data, err := core.ComputeJSONHash(&snap)
	if err != nil {
		t.Fatalf("snapshot ref: %v", err)
	}
	ref := "snapshot/" + hash
	if err := h.store.Put(ctx, ref, data); err != nil {
		t.Fatalf("put snapshot: %v", err)
	}
	if err := h.store.Put(ctx, "index/latest", createIndex(ref, 1)); err != nil {
		t.Fatalf("put latest index: %v", err)
	}
	return h.store
}

func dirMeta(id, name string, parents ...string) core.FileMeta {
	return core.FileMeta{FileID: id, Name: name, Type: core.FileTypeFolder, Parents: parents}
}

func fileMeta(id, name string, parents ...string) core.FileMeta {
	return core.FileMeta{FileID: id, Name: name, Type: core.FileTypeFile, Parents: parents}
}

// restoreToZip runs a restore and returns the archive's entries.
func restoreToZip(t *testing.T, dest *MockStore, opts ...RestoreOption) (map[string]string, *RestoreResult) {
	t.Helper()
	var buf bytes.Buffer
	rm := NewRestoreManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
	res, err := rm.Run(context.Background(), NewZipRestoreWriter(&buf), "latest", opts...)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	return zipEntries(t, &buf), res
}

// The claim the whole change rests on: for a snapshot backup wrote, descending
// the routing prefix reaches every entry, so nothing falls back to the plan.
func TestDerivedWalk_ReachesEverySnapshotEntry(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()

	// A tree wide and deep enough that leaves sit at several levels: the
	// derivation has to hold whether a directory's children share a leaf with
	// nobody, with each other only, or with a colliding directory.
	for d := range 40 {
		dir := fmt.Sprintf("dir%d", d)
		src.Files[dir] = MockFile{Meta: dirMeta(dir, dir)}
		sub := dir + "/sub"
		src.Files[sub] = MockFile{Meta: dirMeta(sub, "sub", dir)}
		for f := range 12 {
			id := fmt.Sprintf("%s/f%d", sub, f)
			src.Files[id] = MockFile{Meta: fileMeta(id, fmt.Sprintf("f%d.txt", f), sub), Content: []byte(id)}
		}
	}

	dest := NewMockStore()
	if _, err := NewBackupManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()}, src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}

	rm := NewRestoreManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
	snap, _, err := rm.resolveSnapshot(ctx, "")
	if err != nil {
		t.Fatalf("resolveSnapshot: %v", err)
	}
	total, err := rm.countEntries(ctx, snap.Root)
	if err != nil {
		t.Fatalf("countEntries: %v", err)
	}

	result := &RestoreResult{}
	out := rm.newEmitter(ctx, nil, restoreConfig{dryRun: true}, result, total)
	walk := &derivedWalk{tree: rm.tree, store: rm.store, metas: rm.metas, out: out}
	reached, err := walk.run(ctx, snap.Root)
	if err != nil {
		t.Fatalf("derived walk: %v", err)
	}

	if reached != total {
		t.Fatalf("derived walk reached %d of %d entries; the restore would have fallen back to the materialised plan", reached, total)
	}
	if want := int64(40 * (2 + 12)); total != want {
		t.Fatalf("snapshot holds %d entries, want %d", total, want)
	}
}

// A pre-affinity repository routes by SHA256(fileID), so no prefix descent can
// find anything. Restore must still produce the whole tree — permanent backward
// compatibility is not conditional on the traversal that happens to be cheapest.
func TestRestore_PreAffinityRoutingStillRestoresEverything(t *testing.T) {
	h := newHandBuiltSnapshot(t)
	legacy := func(fileID string) string { return core.ComputeHash([]byte(fileID)) }

	h.add(t, dirMeta("d", "docs"), "", legacy("d"))
	h.add(t, fileMeta("f1", "a.txt", "d"), "alpha", legacy("f1"))
	h.add(t, fileMeta("f2", "b.txt", "d"), "beta", legacy("f2"))
	dest := h.seal(t)

	entries, res := restoreToZip(t, dest)
	if res.FilesWritten != 2 {
		t.Fatalf("restored %d files, want 2", res.FilesWritten)
	}
	if got := entries["docs/a.txt"]; got != "alpha" {
		t.Errorf("docs/a.txt = %q, want %q", got, "alpha")
	}
	if got := entries["docs/b.txt"]; got != "beta" {
		t.Errorf("docs/b.txt = %q, want %q", got, "beta")
	}
	if _, ok := entries["docs/"]; !ok {
		t.Error("docs/ was not restored")
	}
}

// An entry whose primary parent is not in the snapshot is unreachable by
// descent. restoreOrder kept it; so must this.
func TestRestore_KeepsEntriesWhoseParentIsMissing(t *testing.T) {
	h := newHandBuiltSnapshot(t)
	h.add(t, fileMeta("kept", "orphan.txt", "gone"), "data", AffinityKey("gone", "kept"))
	h.add(t, fileMeta("normal", "root.txt"), "top", AffinityKey("", "normal"))
	dest := h.seal(t)

	entries, res := restoreToZip(t, dest)
	if res.FilesWritten != 2 {
		t.Fatalf("restored %d files, want 2", res.FilesWritten)
	}
	if got := entries["orphan.txt"]; got != "data" {
		t.Errorf("orphan.txt = %q, want %q; an entry with a missing parent was dropped", got, "data")
	}
	if got := entries["root.txt"]; got != "top" {
		t.Errorf("root.txt = %q, want %q", got, "top")
	}
}

// Two folders naming each other cannot be reached from the root at all. Neither
// may be lost, and the walk must not spin looking for them.
func TestRestore_TerminatesAndKeepsAParentCycle(t *testing.T) {
	h := newHandBuiltSnapshot(t)
	h.add(t, dirMeta("a", "a", "b"), "", AffinityKey("b", "a"))
	h.add(t, dirMeta("b", "b", "a"), "", AffinityKey("a", "b"))
	h.add(t, fileMeta("inside", "note.txt", "a"), "cycle", AffinityKey("a", "inside"))
	dest := h.seal(t)

	entries, res := restoreToZip(t, dest)
	if res.DirsWritten != 2 {
		t.Fatalf("restored %d directories, want 2", res.DirsWritten)
	}
	if res.FilesWritten != 1 {
		t.Fatalf("restored %d files, want 1", res.FilesWritten)
	}
	if len(entries) != 3 {
		t.Fatalf("archive holds %d entries, want 3: %v", len(entries), entries)
	}
}

// A snapshot where only part of the tree is derivable — the state an
// in-place format upgrade leaves behind — must restore once, completely, with
// each entry taking whichever path can reach it.
func TestRestore_MixedRoutingRestoresEachEntryExactlyOnce(t *testing.T) {
	h := newHandBuiltSnapshot(t)
	h.add(t, dirMeta("d", "docs"), "", AffinityKey("", "d"))
	h.add(t, fileMeta("new", "new.txt", "d"), "derived", AffinityKey("d", "new"))
	h.add(t, fileMeta("old", "old.txt", "d"), "planned", core.ComputeHash([]byte("old")))
	dest := h.seal(t)

	entries, res := restoreToZip(t, dest)
	if res.FilesWritten != 2 || res.DirsWritten != 1 {
		t.Fatalf("restored %d files and %d dirs, want 2 and 1", res.FilesWritten, res.DirsWritten)
	}
	if res.Warnings != 0 {
		t.Errorf("restore reported %d warnings; an entry was written twice", res.Warnings)
	}
	if got := entries["docs/new.txt"]; got != "derived" {
		t.Errorf("docs/new.txt = %q", got)
	}
	if got := entries["docs/old.txt"]; got != "planned" {
		t.Errorf("docs/old.txt = %q", got)
	}
}

// A directory colliding with another on the 16-bit affinity prefix sees its
// neighbour's entries in its own listing. Claiming them would restore a file
// into the wrong directory.
func TestDerivedWalk_DiscardsPrefixNeighbours(t *testing.T) {
	h := newHandBuiltSnapshot(t)

	// Two folders routed under one prefix by hand, which is what a hash
	// collision produces at 65,536 buckets.
	shared := core.ComputeHash([]byte("left"))[:4]
	h.add(t, dirMeta("left", "left"), "", AffinityKey("", "left"))
	h.add(t, dirMeta("right", "right"), "", AffinityKey("", "right"))
	h.add(t, fileMeta("l1", "l.txt", "left"), "L", shared+core.ComputeHash([]byte("l1"))[4:])
	h.add(t, fileMeta("r1", "r.txt", "right"), "R", shared+core.ComputeHash([]byte("r1"))[4:])
	dest := h.seal(t)

	entries, res := restoreToZip(t, dest)
	if res.FilesWritten != 2 {
		t.Fatalf("restored %d files, want 2", res.FilesWritten)
	}
	if got := entries["left/l.txt"]; got != "L" {
		t.Errorf("left/l.txt = %q, want %q", got, "L")
	}
	// r1 is routed under left's prefix, so no descent finds it under right —
	// the fallback is what keeps it, and it must land at its own path.
	if got := entries["right/r.txt"]; got != "R" {
		t.Errorf("right/r.txt = %q, want %q; a neighbour was claimed by the wrong directory", got, "R")
	}
}

// Derivation must not change what -path selects.
func TestRestore_PathFilterUnderDerivedOrder(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()
	src.Files["keep"] = MockFile{Meta: dirMeta("keep", "keep")}
	src.Files["keep/deep"] = MockFile{Meta: dirMeta("keep/deep", "deep", "keep")}
	src.Files["keep/deep/a"] = MockFile{Meta: fileMeta("keep/deep/a", "a.txt", "keep/deep"), Content: []byte("A")}
	src.Files["keep/b"] = MockFile{Meta: fileMeta("keep/b", "b.txt", "keep"), Content: []byte("B")}
	src.Files["drop"] = MockFile{Meta: dirMeta("drop", "drop")}
	src.Files["drop/c"] = MockFile{Meta: fileMeta("drop/c", "c.txt", "drop"), Content: []byte("C")}

	dest := NewMockStore()
	if _, err := NewBackupManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()}, src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}

	t.Run("subtree", func(t *testing.T) {
		entries, res := restoreToZip(t, dest, WithRestorePath("keep/"))
		if res.FilesWritten != 2 {
			t.Fatalf("restored %d files, want 2", res.FilesWritten)
		}
		for _, want := range []string{"keep/", "keep/deep/", "keep/deep/a.txt", "keep/b.txt"} {
			if _, ok := entries[want]; !ok {
				t.Errorf("%s missing from filtered restore", want)
			}
		}
		if _, ok := entries["drop/"]; ok {
			t.Error("drop/ is outside the filter and was restored")
		}
	})

	t.Run("single file keeps ancestors and nothing else", func(t *testing.T) {
		entries, res := restoreToZip(t, dest, WithRestorePath("keep/deep/a.txt"))
		if res.FilesWritten != 1 {
			t.Fatalf("restored %d files, want 1", res.FilesWritten)
		}
		if len(entries) != 3 {
			t.Fatalf("archive holds %v, want the file and its two ancestors", entries)
		}
		if _, ok := entries["keep/"]; !ok {
			t.Error("ancestor keep/ missing")
		}
		if _, ok := entries["keep/deep/"]; !ok {
			t.Error("ancestor keep/deep/ missing")
		}
	})

	t.Run("no match restores nothing", func(t *testing.T) {
		entries, res := restoreToZip(t, dest, WithRestorePath("absent/"))
		if res.FilesWritten != 0 || res.DirsWritten != 0 {
			t.Fatalf("restored %d files and %d dirs for a filter matching nothing", res.FilesWritten, res.DirsWritten)
		}
		if len(entries) != 0 {
			t.Fatalf("archive holds %v", entries)
		}
	})
}

// What a streaming restore retains is one batch of refs plus the directories it
// has discovered and not yet expanded. Both halves are asserted here as entry
// counts rather than heap measurements, which are too noisy to assert on — and
// together they are the whole claim: restore's memory tracks a snapshot's
// interior and a fixed window, not its file count.

// gather must not hand back an unbounded batch however many directories are
// waiting, which is what turns a stack of pending work back into a plan.
func TestDerivedWalk_GatherBoundsTheBatch(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()

	// Enough entries that one batch cannot hold the tree.
	dirs := 60
	perDir := derivedScanBatch/10 + 1
	for d := range dirs {
		dir := fmt.Sprintf("dir%d", d)
		src.Files[dir] = MockFile{Meta: dirMeta(dir, dir)}
		for f := range perDir {
			id := fmt.Sprintf("%s/f%d", dir, f)
			src.Files[id] = MockFile{Meta: fileMeta(id, fmt.Sprintf("f%d.txt", f), dir), Content: []byte(id)}
		}
	}

	dest := NewMockStore()
	if _, err := NewBackupManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()}, src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}
	rm := NewRestoreManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
	snap, _, err := rm.resolveSnapshot(ctx, "")
	if err != nil {
		t.Fatalf("resolveSnapshot: %v", err)
	}

	walk := &derivedWalk{tree: rm.tree, store: rm.store, metas: rm.metas}
	stack := make([]*restoreDir, 0, dirs)
	for d := range dirs {
		id := fmt.Sprintf("dir%d", d)
		stack = append(stack, &restoreDir{id: id, name: id, made: true})
	}

	total := 0
	for len(stack) > 0 {
		_, refs, err := walk.gather(ctx, snap.Root, &stack)
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		// One directory wider than the batch is still handed over whole — the
		// bound is on how many directories are gathered, not on splitting one.
		if len(refs) > derivedScanBatch+perDir {
			t.Fatalf("gather returned %d refs, past the %d-ref batch", len(refs), derivedScanBatch)
		}
		total += len(refs)
	}
	if total != dirs*perDir {
		t.Fatalf("gather yielded %d refs in total, want %d", total, dirs*perDir)
	}
}

// Only directories survive a batch. A file is written and forgotten, which is
// what makes the frontier grow with a snapshot's interior rather than its size.
func TestDerivedWalk_RetainsOnlyDirectories(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()
	src.Files["d"] = MockFile{Meta: dirMeta("d", "d")}
	for i := range 3 {
		id := fmt.Sprintf("d/sub%d", i)
		src.Files[id] = MockFile{Meta: dirMeta(id, fmt.Sprintf("sub%d", i), "d")}
	}
	for i := range 200 {
		id := fmt.Sprintf("d/f%d", i)
		src.Files[id] = MockFile{Meta: fileMeta(id, fmt.Sprintf("f%d.txt", i), "d"), Content: []byte(id)}
	}

	dest := NewMockStore()
	if _, err := NewBackupManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()}, src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}
	rm := NewRestoreManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
	snap, _, err := rm.resolveSnapshot(ctx, "")
	if err != nil {
		t.Fatalf("resolveSnapshot: %v", err)
	}

	out := rm.newEmitter(ctx, nil, restoreConfig{dryRun: true}, &RestoreResult{}, 0)
	walk := &derivedWalk{tree: rm.tree, store: rm.store, metas: rm.metas, out: out}

	stack := []*restoreDir{{id: "d", name: "d", made: true}}
	scans, refs, err := walk.gather(ctx, snap.Root, &stack)
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	metas, err := walk.read(ctx, refs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	found, err := walk.emit(ctx, scans, metas)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}

	if len(refs) != 203 {
		t.Fatalf("batch held %d refs, want 203", len(refs))
	}
	if len(found) != 3 {
		t.Fatalf("batch retained %d entries, want the 3 directories; files are being held", len(found))
	}
}

func TestDerivedPath_TruncatesLikeTheParentChain(t *testing.T) {
	// A chain deeper than maxParentDepth resolves to its last maxParentDepth+1
	// components, which is what collectMetaPaths does for the same tree.
	var dir *restoreDir
	for i := range 60 {
		dir = &restoreDir{id: fmt.Sprintf("d%d", i), name: fmt.Sprintf("d%d", i), parent: dir}
	}
	got := derivedPath(fileMeta("leaf", "leaf.txt"), dir)

	segments := 1
	for i := 0; i < len(got); i++ {
		if got[i] == '/' {
			segments++
		}
	}
	if segments != maxParentDepth+1 {
		t.Fatalf("path has %d segments, want %d: %s", segments, maxParentDepth+1, got)
	}
}
