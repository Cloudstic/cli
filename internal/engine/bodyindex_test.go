package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"testing"
)

// hashOf is what blob.Writer.Add checks a body against.
func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// touch gives one file a new mtime and the same bytes — the change a `touch`,
// a permissions sweep or a restore-then-rebackup produces, and the one format
// v3 used to answer by storing the body a second time (issue #514).
func touch(src *MockSource, i int) {
	id := fmt.Sprintf("id-%02d", i)
	f := src.Files[id]
	f.Meta.Mtime += 3600
	src.Files[id] = f
}

// duplicateTree is 48 distinct 1 KB bodies, the same fixture the
// consolidation tests age.
func duplicateTree() *MockSource {
	src := NewMockSource()
	for i := range 48 {
		src.AddFile(fmt.Sprintf("f%02d.txt", i), fmt.Sprintf("id-%02d", i), body(int64(i), 1024))
	}
	return src
}

// TestV3Backup_TouchingUnchangedFilesStoresNoNewBodies is the measurement this
// change exists for.
//
// Change detection calls a file with a new mtime *changed*, so v3 read it,
// packed it into a fresh blob and stored bytes it already held: touching 300
// files grew a repository by 3,304 KB against 216 KB for the packfile format.
// Reusing the placement makes the second backup write no blob at all.
func TestV3Backup_TouchingUnchangedFilesStoresNoNewBodies(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src := duplicateTree()

	first, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	blobPuts := dest.PutCount("blob/")
	if blobPuts == 0 {
		t.Fatal("the first backup wrote no blob")
	}

	for i := range 48 {
		touch(src, i)
	}
	second, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if second.FilesChanged != 48 {
		t.Fatalf("the touch was not seen as a change: %d files changed", second.FilesChanged)
	}
	if got := dest.PutCount("blob/") - blobPuts; got != 0 {
		t.Errorf("touching 48 unchanged files wrote %d blob objects, want 0", got)
	}

	// The bodies must still be exactly where they were, and still readable.
	if got, want := snapshotBodies(t, dest, second.SnapshotRef), snapshotBodies(t, dest, first.SnapshotRef); len(got) != len(want) {
		t.Fatalf("second snapshot has %d bodies, first has %d", len(got), len(want))
	} else {
		for id, ref := range want {
			if got[id] != ref {
				t.Fatalf("%s moved from %+v to %+v", id, ref, got[id])
			}
		}
	}
	if !sameTree(restoreZip(t, dest, second.SnapshotRef), restoreZip(t, dest, first.SnapshotRef)) {
		t.Error("the second snapshot restores differently from the first")
	}
}

// TestV3Backup_DuplicateFileCostsNoNewBody covers deduplication that spans
// blobs, which is what a per-blob member set cannot do.
//
// blob.Writer.Add already stores a repeated body once, but only within the
// blob it is packing: two identical files that land either side of a seal were
// stored twice. A one-byte budget seals after every body, so every duplicate
// here crosses a blob boundary.
func TestV3Backup_DuplicateFileCostsNoNewBody(t *testing.T) {
	t.Setenv(envBlobBudget, "1")
	ctx := context.Background()
	dest := NewMockStore()

	src := NewMockSource()
	src.AddFile("a.txt", "id-a", body(1, 4096))
	src.AddFile("b.txt", "id-b", body(2, 4096))
	src.AddFile("copy-of-a.txt", "id-c", body(1, 4096))
	src.AddFile("also-a.txt", "id-d", body(1, 4096))

	res, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	// Counted as writes rather than as distinct keys: three blobs each
	// holding only body(1) have the same member sequence and so the same ref,
	// which a content-addressed store collapses. What is being measured is
	// whether the bytes were packed again, and that is a Put.
	if got := dest.PutCount("blob/"); got != 2 {
		t.Errorf("four files with two distinct bodies wrote %d blobs, want 2", got)
	}

	restored := restoreZip(t, dest, res.SnapshotRef)
	for _, name := range []string{"a.txt", "copy-of-a.txt", "also-a.txt"} {
		if !bytes.Equal(restored[name], body(1, 4096)) {
			t.Errorf("%s restored %d bytes of the wrong content", name, len(restored[name]))
		}
	}
	if !bytes.Equal(restored["b.txt"], body(2, 4096)) {
		t.Error("b.txt restored the wrong content")
	}
}

// TestV3Backup_DuplicateOfAnEarlierBackupCostsNoNewBody is the cross-backup
// half: the copy arrives in a later run, so the placement it reuses comes out
// of the previous snapshot's tree rather than out of this run's writer.
func TestV3Backup_DuplicateOfAnEarlierBackupCostsNoNewBody(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src := NewMockSource()
	src.AddFile("a.txt", "id-a", body(1, 4096))

	if _, err := NewBackupManager(v3Deps(dest), src).Run(ctx); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	blobPuts := dest.PutCount("blob/")

	src.AddFile("copy-of-a.txt", "id-copy", body(1, 4096))
	second, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if second.FilesNew != 1 {
		t.Fatalf("second backup saw %d new files, want 1", second.FilesNew)
	}
	if got := dest.PutCount("blob/") - blobPuts; got != 0 {
		t.Errorf("copying a stored file wrote %d blob objects, want 0", got)
	}

	restored := restoreZip(t, dest, second.SnapshotRef)
	if !bytes.Equal(restored["copy-of-a.txt"], body(1, 4096)) {
		t.Error("the copy restored the wrong content")
	}
}

// TestV3Backup_ReusedBodiesSurviveForgetAndPrune is the correctness bar.
//
// A reused placement points at a blob a *previous* backup wrote. Once the
// snapshot that wrote it is forgotten, the only thing keeping that blob alive
// is the entry that reused it — so prune has to mark it through that entry.
// It does, because v3 marking never deduplicates on an entry's value; this
// pins the behaviour from the side that depends on it.
func TestV3Backup_ReusedBodiesSurviveForgetAndPrune(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src := duplicateTree()

	first, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	for i := range 48 {
		touch(src, i)
	}
	second, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	want := restoreZip(t, dest, second.SnapshotRef)

	if _, err := NewForgetManager(v3Deps(dest)).Run(ctx, first.SnapshotRef); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, err := NewPruneManager(v3Deps(dest)).Run(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if got := dest.CountPrefix("blob/"); got == 0 {
		t.Fatal("prune deleted every blob the surviving snapshot reuses")
	}
	if !sameTree(restoreZip(t, dest, second.SnapshotRef), want) {
		t.Error("the surviving snapshot restores differently after prune")
	}
	res, err := NewCheckManager(v3Deps(dest)).Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("check -read-data after prune: %v", res.Errors)
	}
}

// TestV3Backup_ConsolidationRetiresABlobKeptAliveByReuse covers the one
// interaction that can turn reuse into a regression.
//
// Consolidation retires a blob by moving every live body out of it, and it
// learns which bodies those are from the scan, which only reports entries the
// backup passed over. An entry that *reused* a placement keeps its blob alive
// just as an unchanged entry does — so if it were not reported, the blob would
// look emptier than it is, be selected on that basis, and then survive the
// rewrite anyway, having cost bytes for nothing.
//
// Here every file is either rewritten or touched, so the old blobs' entire
// live content is reuses. If those are invisible to consolidation nothing is
// consolidated at all and the newest snapshot still reads the old blobs.
func TestV3Backup_ConsolidationRetiresABlobKeptAliveByReuse(t *testing.T) {
	// Stated rather than inherited, for the reason the other consolidation
	// fixtures state it: these blobs sit in a narrow band.
	t.Setenv(envConsolidateFill, "50")
	// 16 KB blobs, so 48 files of 1 KB seal three full ones.
	t.Setenv(envBlobBudget, strconv.Itoa(16<<10))

	ctx := context.Background()
	dest := NewMockStore()
	src := duplicateTree()

	first, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	old := snapshotBlobs(t, dest, first.SnapshotRef)
	if len(old) != 3 {
		t.Fatalf("first backup wrote %d blobs, want 3", len(old))
	}

	for i := range 40 {
		churn(src, i, i)
	}
	for i := 40; i < 48; i++ {
		touch(src, i)
	}
	second, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if second.FilesUnmodified != 0 {
		t.Fatalf("%d files were unmodified; the fixture needs every live body to be a reuse", second.FilesUnmodified)
	}

	for _, ref := range snapshotBlobs(t, dest, second.SnapshotRef) {
		if slices.Contains(old, ref) {
			t.Errorf("the newest snapshot still reads %s, which consolidation should have retired", ref)
		}
	}
	// The touched files are the ones whose bodies were reused and then moved
	// forward, so they are what a wrong rewrite corrupts.
	restored := restoreZip(t, dest, second.SnapshotRef)
	for i := 40; i < 48; i++ {
		name := fmt.Sprintf("f%02d.txt", i)
		if !bytes.Equal(restored[name], body(int64(i), 1024)) {
			t.Errorf("%s restored %d bytes of the wrong content", name, len(restored[name]))
		}
	}
	res, err := NewCheckManager(v3Deps(dest)).Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("check -read-data after consolidation over reused bodies: %v", res.Errors)
	}
}

// TestV3Backup_FullBodyIndexDegradesToNoDedup pins what happens at the cap: a
// backup that cannot hold the repository's placements stores the bodies again,
// exactly as it did before this existed. It never fails, and it never returns
// a wrong answer.
func TestV3Backup_FullBodyIndexDegradesToNoDedup(t *testing.T) {
	// Room for nothing at all.
	t.Setenv(envBodyIndex, "1")

	ctx := context.Background()
	dest := NewMockStore()
	src := duplicateTree()

	if _, err := NewBackupManager(v3Deps(dest), src).Run(ctx); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	blobPuts := dest.PutCount("blob/")

	for i := range 48 {
		touch(src, i)
	}
	second, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if got := dest.PutCount("blob/") - blobPuts; got == 0 {
		t.Error("the cap did not stop deduplication; the fixture proves nothing")
	}
	if _, err := NewRestoreManager(v3Deps(dest)).Run(ctx, NewZipRestoreWriter(&bytes.Buffer{}), second.SnapshotRef); err != nil {
		t.Fatalf("restore with a full index: %v", err)
	}
}

// --- the index itself ------------------------------------------------------

func TestBodyIndex_IgnoresUnusableReferences(t *testing.T) {
	x := newBodyIndex()
	x.inherit("", bodyRef("blob/a", 0, 10, 100), 10)
	x.inherit("h1", nil, 10)
	x.inherit("h2", bodyRef("", 0, 10, 100), 10)
	x.inherit("h3", bodyRef("blob/a", 0, 10, 0), 10)    // no denominator
	x.inherit("h4", bodyRef("blob/a", 0, 0, 100), 10)   // no extent
	x.inherit("h5", bodyRef("blob/a", -1, 10, 100), 10) // impossible offset
	x.inherit("h6", bodyRef("blob/a", 0, 10, 100), 0)   // no plaintext size
	if len(x.placed) != 0 {
		t.Fatalf("recorded %d unusable placements", len(x.placed))
	}
}

func TestBodyIndex_KeepsTheFirstPlacementForAHash(t *testing.T) {
	x := newBodyIndex()
	first := bodyRef("blob/a", 0, 10, 100)
	x.inherit("h", first, 10)
	x.inherit("h", bodyRef("blob/b", 0, 10, 100), 10)

	p := x.lookup("h", 10)
	if p == nil {
		t.Fatal("no placement for a hash that was recorded")
	}
	if got := p.placed(); got == nil || *got != *first {
		t.Fatalf("lookup returned %+v, want %+v", got, first)
	}
	if !p.inherited {
		t.Error("a placement read out of the previous tree is not marked inherited")
	}
	if hits, bytes, entries := x.stats(); hits != 1 || bytes != 10 || entries != 1 {
		t.Errorf("stats = %d hits, %d bytes, %d entries", hits, bytes, entries)
	}

	// A size that disagrees with the recorded body is not a hit. On the
	// pre-read path the size is the source's claim rather than something this
	// run measured, and reusing a placement under a wrong one writes an entry
	// that restores wrongly.
	if x.lookup("h", 11) != nil {
		t.Error("a placement was reused under a size it does not hold")
	}
}

func TestBodyIndex_StopsRecordingAtItsCap(t *testing.T) {
	x := newBodyIndex()
	x.limit = approxPlacementSize("h0", "blob/a") // room for exactly one
	x.inherit("h0", bodyRef("blob/a", 0, 10, 100), 10)
	x.inherit("h1", bodyRef("blob/a", 10, 10, 100), 10)

	if x.lookup("h0", 10) == nil {
		t.Error("the placement recorded before the cap was lost")
	}
	if x.lookup("h1", 10) != nil {
		t.Error("a placement was recorded past the cap")
	}
	if x.held > x.limit {
		t.Errorf("index holds %d bytes against a %d-byte cap", x.held, x.limit)
	}
}

// TestBodyIndex_NilIsNoDedup covers the shape every caller relies on: outside
// format v3, and on a dry run, there is no index and the calls still work.
func TestBodyIndex_NilIsNoDedup(t *testing.T) {
	var x *bodyIndex
	x.inherit("h", bodyRef("blob/a", 0, 10, 100), 10)
	if x.lookup("h", 10) != nil {
		t.Error("a nil index returned a placement")
	}
	if hits, bytes, entries := x.stats(); hits != 0 || bytes != 0 || entries != 0 {
		t.Errorf("a nil index reported %d hits, %d bytes, %d entries", hits, bytes, entries)
	}

	w := newBlobWriter(NewMockStore(), nil)
	p, err := x.place(context.Background(), w, hashOf([]byte("hello")), []byte("hello"))
	if err != nil {
		t.Fatalf("place through a nil index: %v", err)
	}
	if p == nil {
		t.Fatal("place through a nil index returned no promise")
	}
	if p.inherited {
		t.Error("a body handed to the writer is marked inherited")
	}
}

// TestBodyIndex_PlaceRecordsWhatItPacks pins the run-local half: the second
// offer of the same body gets the first one's promise, so both entries resolve
// to one member.
func TestBodyIndex_PlaceRecordsWhatItPacks(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	w := newBlobWriter(dest, nil)
	x := newBodyIndex()

	data := []byte("the same bytes twice")
	first, err := x.place(ctx, w, hashOf(data), data)
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	second, err := x.place(ctx, w, hashOf(data), data)
	if err != nil {
		t.Fatalf("place again: %v", err)
	}
	if first != second {
		t.Error("the second offer of one body got a different promise")
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if first.placed() == nil {
		t.Fatal("the promise was never resolved")
	}
	if dest.CountPrefix("blob/") != 1 {
		t.Errorf("one body sealed %d blobs", dest.CountPrefix("blob/"))
	}
}
