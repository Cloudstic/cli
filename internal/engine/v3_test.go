package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/blob"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// v3Deps is the dependency set every manager in these tests is built from:
// one store, format v3.
func v3Deps(dest *MockStore) Deps {
	// BlobStore is the same store here because a MockStore has no format chain
	// to sit below. In a real client the two differ: blobs go under
	// compression and encryption, carrying their own of each per member.
	return Deps{Store: dest, Reporter: ui.NewNoOpReporter(), FormatV3: true, BlobStore: dest}
}

// v3Source builds a small tree with every content shape the format stores:
// folders, small files that inline into leaves, and one file large enough to
// be chunked (the inline threshold is 512 KiB).
func v3Source() (*MockSource, []byte) {
	src := NewMockSource()
	src.Files["dir1"] = MockFile{
		Meta: core.FileMeta{FileID: "dir1", Name: "docs", Type: core.FileTypeFolder},
	}
	src.AddFile("a.txt", "id-a", []byte("alpha"))
	src.AddFile("b.txt", "id-b", []byte("beta beta"))
	nested := MockFile{
		Meta: core.FileMeta{
			FileID: "id-c", Name: "c.txt", Type: core.FileTypeFile,
			Parents: []string{"dir1"}, Size: 5, Mtime: time.Now().Unix(),
		},
		Content: []byte("gamma"),
	}
	src.Files["id-c"] = nested

	big := make([]byte, 600*1024)
	rand.New(rand.NewSource(7)).Read(big)
	src.AddFile("big.bin", "id-big", big)
	return src, big
}

// assertV3Physical fails when the store holds any object a v3 repository must
// not have.
func assertV3Physical(t *testing.T, dest *MockStore) {
	t.Helper()
	for key := range dest.Data {
		for _, prefix := range []string{"filemeta/", "content/", "packs/", "index/packs"} {
			if strings.HasPrefix(key, prefix) {
				t.Errorf("v3 store holds %s", key)
			}
		}
	}
}

// TestV3Engine_BackupRestoreCheckPrune drives the engine managers directly
// through a v3 cycle, covering the leaf-payload paths of each.
func TestV3Engine_BackupRestoreCheckPrune(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src, big := v3Source()

	res, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res.FilesNew == 0 || res.DirsNew == 0 {
		t.Fatalf("backup stats: files=%d dirs=%d", res.FilesNew, res.DirsNew)
	}
	assertV3Physical(t, dest)

	// Restore through the zip writer (the sequential path) and verify content
	// from both storage shapes came back intact.
	var buf bytes.Buffer
	if _, err := NewRestoreManager(v3Deps(dest)).Run(ctx, NewZipRestoreWriter(&buf), "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open restored zip: %v", err)
	}
	restored := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		restored[f.Name] = data
	}
	if string(restored["a.txt"]) != "alpha" {
		t.Errorf("restored a.txt = %q", restored["a.txt"])
	}
	if !bytes.Equal(restored["big.bin"], big) {
		t.Error("restored big.bin differs")
	}
	if string(restored["docs/c.txt"]) != "gamma" {
		t.Errorf("restored docs/c.txt = %q", restored["docs/c.txt"])
	}

	// Check, both passes.
	for _, opts := range [][]CheckOption{nil, {WithReadData()}} {
		checkRes, err := NewCheckManager(v3Deps(dest)).Run(ctx, opts...)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		if len(checkRes.Errors) != 0 {
			t.Fatalf("check errors: %v", checkRes.Errors)
		}
	}

	// An incremental backup with one change, one addition, one deletion:
	// unchanged entries must carry their payloads forward.
	src.AddFile("a.txt", "id-a", []byte("alpha v2"))
	src.AddFile("new.txt", "id-new", []byte("fresh"))
	delete(src.Files, "id-b")
	res2, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("incremental backup: %v", err)
	}
	if res2.FilesUnmodified == 0 || res2.FilesChanged == 0 || res2.FilesNew == 0 || res2.FilesRemoved == 0 {
		t.Fatalf("incremental stats: %+v", res2)
	}

	// Diff between the two snapshots resolves metadata from payloads.
	diffRes, err := NewDiffManager(v3Deps(dest)).Run(ctx, res.SnapshotRef, res2.SnapshotRef)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diffRes.Changes) < 3 {
		t.Fatalf("diff reported %d changes", len(diffRes.Changes))
	}

	// Ls the latest snapshot.
	lsRes, err := NewLsSnapshotManager(v3Deps(dest)).Run(ctx, "latest")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if len(lsRes.RefToMeta) == 0 {
		t.Fatal("ls returned no entries")
	}

	// Prune with both snapshots live must not delete reachable data...
	if _, err := NewPruneManager(v3Deps(dest)).Run(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}
	var out2 bytes.Buffer
	if _, err := NewRestoreManager(v3Deps(dest)).Run(ctx, NewZipRestoreWriter(&out2), "latest"); err != nil {
		t.Fatalf("restore after prune: %v", err)
	}

	// ...and after forgetting the first snapshot it must collect its garbage.
	if _, err := NewForgetManager(v3Deps(dest)).Run(ctx, res.SnapshotRef); err != nil {
		t.Fatalf("forget: %v", err)
	}
	pruneRes, err := NewPruneManager(v3Deps(dest)).Run(ctx)
	if err != nil {
		t.Fatalf("prune after forget: %v", err)
	}
	if pruneRes.ObjectsDeleted == 0 {
		t.Error("prune after forget deleted nothing")
	}
	checkRes, err := NewCheckManager(v3Deps(dest)).Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("final check: %v", err)
	}
	if len(checkRes.Errors) != 0 {
		t.Fatalf("final check errors: %v", checkRes.Errors)
	}
}

// v3Snapshot writes a snapshot object over root so the read managers can find
// a hand-built tree.
func v3Snapshot(t *testing.T, dest *MockStore, root string) string {
	t.Helper()
	snap := core.Snapshot{Version: 1, Created: time.Now().Format(time.RFC3339), Root: root, Seq: 1}
	hash, data, err := core.ComputeJSONHash(&snap)
	if err != nil {
		t.Fatal(err)
	}
	ref := "snapshot/" + hash
	if err := dest.Put(context.Background(), ref, data); err != nil {
		t.Fatal(err)
	}
	idxData := fmt.Appendf(nil, `{"latest_snapshot":%q,"seq":1}`, ref)
	if err := dest.Put(context.Background(), "index/latest", idxData); err != nil {
		t.Fatal(err)
	}
	return ref
}

// insertV3Entry files meta into a fresh v3 tree with the given payload shape
// and returns the committed root.
func insertV3Entry(t *testing.T, dest *MockStore, value string, p *hamt.Payload) string {
	t.Helper()
	tree := hamt.NewTree(dest, hamt.WithFormatV3())
	tx := tree.Edit("")
	routing := core.ComputeHash([]byte("route"))
	if err := tx.InsertWithPayload(context.Background(), routing, "id-x", value, p); err != nil {
		t.Fatal(err)
	}
	root, err := tx.Commit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// metaFor returns the canonical bytes and content address of a minimal
// filemeta.
func metaFor(t *testing.T, fm core.FileMeta) (string, []byte) {
	t.Helper()
	ref, data, err := core.FileMetaRef(&fm)
	if err != nil {
		t.Fatal(err)
	}
	return ref, data
}

// TestV3Check_ReportsBrokenLeafEntries covers checkLeafEntry's findings: a
// payload-less entry, meta bytes that do not hash to the entry's value, a
// content_ref that does not derive from content_hash, and a reconstructed
// content that misses the recorded hash.
func TestV3Check_ReportsBrokenLeafEntries(t *testing.T) {
	ctx := context.Background()

	run := func(t *testing.T, dest *MockStore, hmacKey []byte, opts ...CheckOption) *CheckResult {
		t.Helper()
		d := v3Deps(dest)
		d.HMACKey = hmacKey
		res, err := NewCheckManager(d).Run(ctx, opts...)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		return res
	}

	wantFinding := func(t *testing.T, res *CheckResult, typ, fragment string) {
		t.Helper()
		for _, e := range res.Errors {
			if e.Type == typ && strings.Contains(e.Message, fragment) {
				return
			}
		}
		t.Fatalf("no %q finding mentioning %q in %v", typ, fragment, res.Errors)
	}

	t.Run("missing payload", func(t *testing.T) {
		dest := NewMockStore()
		ref, _ := metaFor(t, core.FileMeta{FileID: "id-x", Name: "x", Type: core.FileTypeFile})
		root := insertV3Entry(t, dest, ref, nil)
		v3Snapshot(t, dest, root)
		wantFinding(t, run(t, dest, nil), "missing", "no payload")
	})

	t.Run("meta does not hash to value", func(t *testing.T) {
		dest := NewMockStore()
		ref, _ := metaFor(t, core.FileMeta{FileID: "id-x", Name: "x", Type: core.FileTypeFile})
		root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: []byte(`{"forged":true}`)})
		v3Snapshot(t, dest, root)
		wantFinding(t, run(t, dest, nil), "corrupt", "")
	})

	t.Run("content_ref does not derive from content_hash", func(t *testing.T) {
		dest := NewMockStore()
		hmacKey := bytes.Repeat([]byte{1}, 32)
		fm := core.FileMeta{
			FileID: "id-x", Name: "x", Type: core.FileTypeFile,
			ContentHash: core.ComputeHash([]byte("data")), ContentRef: "not-derived", Size: 4,
		}
		ref, data := metaFor(t, fm)
		root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: 4, Body: blobFor(t, dest, []byte("data"))})
		v3Snapshot(t, dest, root)
		wantFinding(t, run(t, dest, hmacKey), "corrupt", "does not derive")
	})

	t.Run("reconstructed content misses the recorded hash", func(t *testing.T) {
		dest := NewMockStore()
		fm := core.FileMeta{
			FileID: "id-x", Name: "x", Type: core.FileTypeFile,
			ContentHash: core.ComputeHash([]byte("original")), Size: 8,
		}
		ref, data := metaFor(t, fm)
		root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: 8, Body: blobFor(t, dest, []byte("tampered"))})
		v3Snapshot(t, dest, root)
		// The mismatch is caught reading the member rather than after
		// reconstructing it: a body's content hash is what keys its seal, so
		// a body that is not the one the entry names fails at the read.
		wantFinding(t, run(t, dest, nil, WithReadData()), "corrupt", "hashes to")
	})

	t.Run("leaf size disagrees with filemeta", func(t *testing.T) {
		dest := NewMockStore()
		body := []byte("data")
		fm := core.FileMeta{
			FileID: "id-x", Name: "x", Type: core.FileTypeFile,
			ContentHash: core.ComputeHash(body), Size: int64(len(body)),
		}
		ref, data := metaFor(t, fm)
		root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: 999, Body: blobFor(t, dest, body)})
		v3Snapshot(t, dest, root)
		wantFinding(t, run(t, dest, nil, WithReadData()), "corrupt", "records size")
	})
}

// TestV3Restore_FailsOnMissingPayload pins restore's refusal to invent
// content: a v3 entry without a payload is an error in the metadata pass.
func TestV3Restore_FailsOnMissingPayload(t *testing.T) {
	dest := NewMockStore()
	ref, _ := metaFor(t, core.FileMeta{FileID: "id-x", Name: "x", Type: core.FileTypeFile})
	root := insertV3Entry(t, dest, ref, nil)
	v3Snapshot(t, dest, root)

	var buf bytes.Buffer
	_, err := NewRestoreManager(v3Deps(dest)).Run(context.Background(), NewZipRestoreWriter(&buf), "latest")
	if err == nil || !strings.Contains(err.Error(), "no metadata payload") {
		t.Fatalf("restore of a payload-less v3 entry: err=%v", err)
	}
}

// TestV3Prune_RefusesPayloadlessEntry pins the compatibility rule: prune must
// not collect garbage over entries whose chunk refs it could not read.
func TestV3Prune_RefusesPayloadlessEntry(t *testing.T) {
	dest := NewMockStore()
	ref, _ := metaFor(t, core.FileMeta{FileID: "id-x", Name: "x", Type: core.FileTypeFile})
	root := insertV3Entry(t, dest, ref, nil)
	v3Snapshot(t, dest, root)

	_, err := NewPruneManager(v3Deps(dest)).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to prune") {
		t.Fatalf("prune over a payload-less v3 entry: err=%v", err)
	}
}

// A v3 backup must not hold every file it writes until the end: the working
// tree carries each entry's content, so without an intermediate commit the
// phase's memory is the run's total inlined bytes rather than a working set
// (#526).
//
// The bound is asserted through its observable signature rather than through
// peak RSS, which is not measurable from here. Committing mid-run seals a
// spine that later inserts then supersede, and because nodes are
// content-addressed those superseded versions are *different keys* — so they
// stay in the store as garbage that the final root does not reach. A backup
// that committed once leaves none.
func TestV3BackupCommitsIncrementally(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src := NewMockSource()

	// A payload is metadata and a body reference now, so the natural threshold
	// is hundreds of thousands of entries. Lowered to prove the mechanism,
	// which is what this test is about — see envUploadCommitBytes.
	// Both thresholds are lowered together, and they have to be: the commit
	// bound alone would rewrite one leaf over and over, because 400 entries of
	// metadata fit in a single leaf at the real budget. A tree with several
	// leaves is what makes "how much garbage does committing leave" a
	// meaningful question at all.
	t.Setenv("CLOUDSTIC_TEST_LEAF_BYTES", "2048")
	t.Setenv(envUploadCommitBytes, "32768")
	// Distinct bodies, deliberately. Identical ones deduplicate to a single
	// blob member, so the blob never fills and every entry waits on it — the
	// case that forced the entry-count seal in flush.
	for i := range 400 {
		body := append(bytes.Repeat([]byte("payload"), 40*1024), fmt.Appendf(nil, "-%d", i)...)
		src.AddFile(fmt.Sprintf("f-%d.bin", i), fmt.Sprintf("id-%d", i), body)
	}

	res, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	snap, err := loadSnapshotByRef(ctx, dest, res.SnapshotRef)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	tree := hamt.NewTree(dest, hamt.WithFormatV3())
	reachable := 0
	if err := tree.NodeRefs(ctx, snap.Root, func(string) error { reachable++; return nil }); err != nil {
		t.Fatalf("walk nodes: %v", err)
	}
	total := dest.CountPrefix("node/")

	if total <= reachable {
		t.Errorf("%d node objects stored and %d reachable: the upload phase never "+
			"committed mid-run, so it held every payload to the end", total, reachable)
	}

	// The garbage is the price of the bound, not a licence for any amount of
	// it: a run committing on every batch would leave far more than it keeps.
	if total > 3*reachable {
		t.Errorf("%d node objects stored against %d reachable — committing far too often", total, reachable)
	}

	// And the result must still be exactly right.
	checkRes, err := NewCheckManager(v3Deps(dest)).Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(checkRes.Errors) != 0 {
		t.Fatalf("check after incremental commits: %v", checkRes.Errors)
	}
}

// check reads each node once, through the walk that is about to decode it
// anyway — NodeStore.load already verifies a node against its ref, since a
// node ref is the SHA-256 of its bytes. Reading it a second time to verify it
// separately doubled the node reads of every traversal.
//
// The saving is only safe if detection survives it, which is what this pins:
// a repository with several damaged nodes must report *all* of them, not stop
// at the first, and must still fail.
func TestV3CheckReportsEveryDamagedNode(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src := NewMockSource()
	// Enough entries to build a tree with several leaves under a spine. The
	// leaf budget is lowered because a leaf holds metadata now: at the real
	// budget these 200 entries fit in one leaf and there would be nothing to
	// damage twice.
	t.Setenv("CLOUDSTIC_TEST_LEAF_BYTES", "4096")
	body := bytes.Repeat([]byte("content"), 40*1024)
	for i := range 200 {
		src.AddFile(fmt.Sprintf("f-%d.bin", i), fmt.Sprintf("id-%d", i), body)
	}
	if _, err := NewBackupManager(v3Deps(dest), src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res, err := NewCheckManager(v3Deps(dest)).Run(ctx); err != nil || len(res.Errors) != 0 {
		t.Fatalf("baseline check: err=%v errors=%v", err, res.Errors)
	}

	// Damage two leaves in different ways: one whose bytes no longer match its
	// ref, one that is gone entirely.
	var leaves []string
	for key, data := range dest.Data {
		if strings.HasPrefix(key, "node/") && len(data) > 512 {
			leaves = append(leaves, key)
		}
	}
	sort.Strings(leaves) // deterministic choice
	if len(leaves) < 2 {
		t.Fatalf("expected several sizeable leaves to damage, got %d", len(leaves))
	}
	dest.Data[leaves[0]] = []byte("not the bytes this ref names")
	delete(dest.Data, leaves[1])

	res, err := NewCheckManager(v3Deps(dest)).Run(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Errors) < 2 {
		t.Fatalf("check reported %d errors for two damaged nodes: %v\n"+
			"a check that stops at the first fault leaves the rest of the repository unexamined",
			len(res.Errors), res.Errors)
	}

	reported := map[string]bool{}
	for _, e := range res.Errors {
		reported[e.Key] = true
	}
	for _, want := range leaves[:2] {
		if !reported[want] {
			t.Errorf("damaged node %s was not reported; findings: %v", want, res.Errors)
		}
	}
}

// The point of the override is that it moves bodies out of leaves. Asserting
// on inlineLimit() alone would pass while the threshold branch still read the
// constant, which is the whole mechanism.
func TestInlineLimitOverrideChunksSmallBodies(t *testing.T) {
	ctx := context.Background()

	// A body far below the built-in threshold: inlined by default.
	inlined := NewMockStore()
	src := NewMockSource()
	src.AddFile("small.txt", "id-small", []byte("a small body, well under 512 KiB"))
	if _, err := NewBackupManager(v3Deps(inlined), src).Run(ctx); err != nil {
		t.Fatalf("default backup: %v", err)
	}

	// The same body with the threshold at 1 must be chunked instead.
	t.Setenv(envInlineThreshold, "1")
	chunked := NewMockStore()
	src2 := NewMockSource()
	src2.AddFile("small.txt", "id-small", []byte("a small body, well under 512 KiB"))
	if _, err := NewBackupManager(v3Deps(chunked), src2).Run(ctx); err != nil {
		t.Fatalf("override backup: %v", err)
	}

	countChunks := func(s *MockStore) int {
		n := 0
		for k := range s.Data {
			if strings.HasPrefix(k, "chunk/") {
				n++
			}
		}
		return n
	}
	if got := countChunks(inlined); got != 0 {
		t.Errorf("default: %d chunk objects, want the body inlined into its leaf", got)
	}
	if got := countChunks(chunked); got == 0 {
		t.Error("override: no chunk objects, so the threshold branch ignored it")
	}
}

// The inline threshold decides whether a body lives in its leaf or in chunk
// objects, which is the variable a leaf's composition turns on. The override
// exists so that can be varied without a rebuild — measuring what a
// metadata-only tree costs needs a real one, not an extrapolation from the
// byte budget (RFC 0026).
func TestInlineLimitOverride(t *testing.T) {
	if got := inlineLimit(); got != inlineThreshold {
		t.Errorf("unset: inlineLimit() = %d, want the built-in %d", got, inlineThreshold)
	}

	t.Setenv(envInlineThreshold, "1")
	if got := inlineLimit(); got != 1 {
		t.Errorf("override: inlineLimit() = %d, want 1", got)
	}

	// Zero is a legitimate setting — it chunks every body, including empty
	// ones — so it must not be confused with "unset".
	t.Setenv(envInlineThreshold, "0")
	if got := inlineLimit(); got != 0 {
		t.Errorf("zero: inlineLimit() = %d, want 0", got)
	}

	// A malformed or negative value falls back rather than failing a backup:
	// these are diagnostic knobs and a typo in one must not change what is
	// written, let alone stop it.
	for _, bad := range []string{"", "nonsense", "-1", "12.5"} {
		t.Setenv(envInlineThreshold, bad)
		if got := inlineLimit(); got != inlineThreshold {
			t.Errorf("%q: inlineLimit() = %d, want the built-in %d", bad, got, inlineThreshold)
		}
	}
}

// blobFor packs one body into a blob in s and returns the reference a leaf
// entry would carry. Unsealed, matching a MockStore with no encryption key.
func blobFor(t *testing.T, s store.ObjectStore, body []byte) *hamt.BodyRef {
	t.Helper()
	w := blob.NewWriter(nil)
	if err := w.Add(core.ComputeHash(body), body); err != nil {
		t.Fatalf("add body to blob: %v", err)
	}
	ref, data, members, err := w.Seal()
	if err != nil {
		t.Fatalf("seal blob: %v", err)
	}
	if err := s.Put(context.Background(), ref, data); err != nil {
		t.Fatalf("put %s: %v", ref, err)
	}
	return &hamt.BodyRef{
		Blob:   ref,
		Offset: members[0].Offset,
		Length: members[0].Length,
		Total:  int64(len(data)),
	}
}

// Two snapshots can hold entries with the same metadata ref pointing at
// different blobs: identical metadata says nothing about where the body was
// packed, and a re-upload puts the same bytes into whatever blob is open.
//
// The mark used to deduplicate on that ref and skip the second entry's
// objects, so the sweep deleted a blob a retained snapshot still needed. This
// is the shape of that bug, built directly rather than through a backup,
// because reproducing it through the upload path depends on packing timing.
func TestV3Prune_MarksEveryPayloadEvenWhenTheMetadataRefRepeats(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()

	body := []byte("one body, packed twice into different blobs")
	fm := core.FileMeta{
		FileID: "id-x", Name: "x", Type: core.FileTypeFile,
		ContentHash: core.ComputeHash(body), Size: int64(len(body)),
	}
	ref, data := metaFor(t, fm)

	// The same entry value in two snapshots, each naming its own blob.
	firstBlob := blobFor(t, dest, body)
	rootA := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: fm.Size, Body: firstBlob})
	snapA := v3Snapshot(t, dest, rootA)

	// A second blob holding the body alongside other content, as a re-upload
	// into a different run of the walk would produce.
	secondBlob := blobFor(t, dest, append([]byte("padding"), body...))
	if secondBlob.Blob == firstBlob.Blob {
		t.Fatal("fixture produced one blob, not two")
	}
	rootB := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: fm.Size, Body: secondBlob})
	snapB := v3Snapshot(t, dest, rootB)

	if snapA == snapB {
		t.Fatal("fixture produced one snapshot, not two")
	}

	if _, err := NewPruneManager(v3Deps(dest)).Run(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}

	for _, b := range []string{firstBlob.Blob, secondBlob.Blob} {
		if exists, _ := dest.Exists(ctx, b); !exists {
			t.Errorf("prune deleted %s, which a retained snapshot still references", b)
		}
	}
}

// A default check must notice a missing blob. The per-entry chunk loop is the
// only existence check a default run makes over an entry's content, and a
// body-referencing entry has no chunks — so without an equivalent for blob/, a
// repository missing every body reports healthy. -read-data is not a
// substitute: nothing requires a user to run it before trusting a check.
func TestV3Check_ReportsAMissingBlobWithoutReadData(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()

	body := []byte("a body that will go missing")
	fm := core.FileMeta{
		FileID: "id-x", Name: "x", Type: core.FileTypeFile,
		ContentHash: core.ComputeHash(body), Size: int64(len(body)),
	}
	ref, data := metaFor(t, fm)
	b := blobFor(t, dest, body)
	root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: fm.Size, Body: b})
	v3Snapshot(t, dest, root)

	// Healthy first, so the finding below is the deletion and not the fixture.
	if res, err := NewCheckManager(v3Deps(dest)).Run(ctx); err != nil || len(res.Errors) != 0 {
		t.Fatalf("baseline check: err=%v errors=%v", err, res.Errors)
	}

	delete(dest.Data, b.Blob)

	res, err := NewCheckManager(v3Deps(dest)).Run(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("a default check reported a repository healthy after its only blob was deleted")
	}
}

// An entry naming bytes past the end of its blob is corruption a default run
// must catch, and catching it costs no read: the blob's size is enough.
func TestV3Check_ReportsARangePastTheEndOfItsBlob(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()

	body := []byte("a body whose entry will overreach")
	fm := core.FileMeta{
		FileID: "id-x", Name: "x", Type: core.FileTypeFile,
		ContentHash: core.ComputeHash(body), Size: int64(len(body)),
	}
	ref, data := metaFor(t, fm)
	b := blobFor(t, dest, body)
	b.Length += 4096
	root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: fm.Size, Body: b})
	v3Snapshot(t, dest, root)

	res, err := NewCheckManager(v3Deps(dest)).Run(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("a default check accepted an entry naming bytes past the end of its blob")
	}
}

// A body between inlineThreshold and a raised CLOUDSTIC_TEST_INLINE_BYTES must
// survive a round trip.
//
// The read buffer used to be sized from the constant while the routing
// decision used the override, so io.ReadFull filled the short buffer, reported
// no error, and stored a truncated body under the hash of what it had read.
// The repository was self-consistent — check passed — and restore returned a
// short file. That is the worst failure shape there is, and it is why this
// asserts on the restored bytes rather than on any internal count.
func TestV3Backup_InlineBodyLargerThanTheConstantSurvives(t *testing.T) {
	ctx := context.Background()
	t.Setenv(envInlineThreshold, strconv.Itoa(4<<20))

	dest := NewMockStore()
	src := NewMockSource()
	// Comfortably past inlineThreshold (512 KiB) and under the override.
	body := bytes.Repeat([]byte("inline-body-"), 120*1024) // ~1.4 MB
	src.AddFile("big.bin", "id-big", body)

	res, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	_ = res
	root := t.TempDir()
	w, err := NewFSRestoreWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRestoreManager(v3Deps(dest)).Run(ctx, w, "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "big.bin"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("restored %d bytes, want %d — the body was truncated", len(got), len(body))
	}
	if !bytes.Equal(got, body) {
		t.Fatal("restored body differs from the source")
	}
}
