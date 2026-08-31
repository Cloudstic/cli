package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"slices"
	"strconv"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
)

// body returns deterministic incompressible bytes, so that a blob's stored
// size tracks the plaintext budget it was sealed against — a compressible
// fixture would make every blob look tiny next to a budget counted in
// plaintext, and the test would then be measuring zstd.
func body(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func bodyRef(blob string, offset, length, total int64) *hamt.BodyRef {
	return &hamt.BodyRef{Blob: blob, Offset: offset, Length: length, Total: total}
}

// churn replaces one file's content in round r. The mtime moves with it,
// because a MockSource file carries no content hash and change detection
// falls back to observable metadata — two same-sized bodies written in the
// same second are otherwise indistinguishable to it.
func churn(src *MockSource, i, r int) {
	id := fmt.Sprintf("id-%02d", i)
	f := src.Files[id]
	f.Content = body(int64(1000+r), 1024)
	f.Meta.Mtime += int64(r) + 1
	src.Files[id] = f
}

// noteBlob records n entries of size each against one blob, as a walk would.
func noteBlob(c *blobConsolidator, blob string, n int, size, total int64) {
	for i := range n {
		c.note(fmt.Sprintf("key-%s-%02d", blob, i), fmt.Sprintf("id-%s-%02d", blob, i),
			bodyRef(blob, int64(i)*size, size, total))
	}
}

func TestBlobConsolidator_LeavesAPackedRepositoryAlone(t *testing.T) {
	c := newBlobConsolidator()
	// Four blobs, every one of them full.
	for i := range 4 {
		noteBlob(c, fmt.Sprintf("blob/%d", i), 10, 100, 1000)
	}
	if plan := c.plan(); len(plan.blobs) != 0 {
		t.Fatalf("planned %v over blobs that are already full", plan.blobs)
	}
}

func TestBlobConsolidator_SelectsTheEmptiestFirst(t *testing.T) {
	c := newBlobConsolidator()
	noteBlob(c, "blob/full", 10, 100, 1000) // 100% of a full blob
	noteBlob(c, "blob/half", 5, 100, 1000)  // exactly at the mark: left alone
	noteBlob(c, "blob/thin", 1, 100, 1000)  // 10%
	noteBlob(c, "blob/small", 2, 100, 200)  // sealed small: 100% used, 20% of a blob
	noteBlob(c, "blob/third", 3, 100, 1000) // 30%

	plan := c.plan()
	want := []string{"blob/thin", "blob/small", "blob/third"}
	if !slices.Equal(plan.blobs, want) {
		t.Fatalf("planned %v, want %v", plan.blobs, want)
	}
	if plan.bytes != 100+200+300 {
		t.Errorf("plan rewrites %d bytes, want 600", plan.bytes)
	}
	if len(plan.entries) != 6 {
		t.Errorf("plan moves %d entries, want 6", len(plan.entries))
	}
	// Entries come out in routing-key order, whatever order they were walked.
	if !slices.IsSortedFunc(plan.entries, func(a, b blobEntry) int {
		return bytes.Compare([]byte(a.routingKey), []byte(b.routingKey))
	}) {
		t.Error("plan entries are not in routing-key order")
	}
}

func TestBlobConsolidator_BudgetBoundsOneBackup(t *testing.T) {
	t.Setenv(envConsolidateRewrite, "600")
	c := newBlobConsolidator()
	noteBlob(c, "blob/full", 10, 100, 1000)
	noteBlob(c, "blob/a", 1, 100, 1000)
	noteBlob(c, "blob/b", 2, 100, 1000)
	noteBlob(c, "blob/c", 3, 100, 1000)

	plan := c.plan()
	if !slices.Equal(plan.blobs, []string{"blob/a", "blob/b", "blob/c"}) {
		t.Fatalf("planned %v", plan.blobs)
	}
	if plan.bytes != 600 {
		t.Fatalf("plan rewrites %d bytes", plan.bytes)
	}

	// One blob further down and the budget stops before it.
	t.Setenv(envConsolidateRewrite, "300")
	plan = c.plan()
	if !slices.Equal(plan.blobs, []string{"blob/a", "blob/b"}) {
		t.Fatalf("planned %v under a 300-byte budget", plan.blobs)
	}
	if plan.bytes != 300 {
		t.Fatalf("plan rewrites %d bytes under a 300-byte budget", plan.bytes)
	}
}

func TestBlobConsolidator_RefusesToRewriteASingleBlob(t *testing.T) {
	c := newBlobConsolidator()
	noteBlob(c, "blob/full", 10, 100, 1000)
	noteBlob(c, "blob/thin", 1, 100, 1000)
	// One sparse blob rewritten into one new blob leaves the snapshot reading
	// as many objects as before, so it is not worth the bytes.
	if plan := c.plan(); len(plan.blobs) != 0 {
		t.Fatalf("planned %v for a single sparse blob", plan.blobs)
	}
}

func TestBlobConsolidator_CountsASharedMemberOnce(t *testing.T) {
	c := newBlobConsolidator()
	// Two blobs, each holding one member that eight entries share — the shape
	// a tree of duplicate files produces. Counted per entry, each would look
	// like 800 of 1000 bytes live and neither would be consolidated.
	for _, blob := range []string{"blob/dup1", "blob/dup2"} {
		for i := range 8 {
			c.note(fmt.Sprintf("key-%s-%d", blob, i), fmt.Sprintf("id-%s-%d", blob, i),
				bodyRef(blob, 0, 100, 1000))
		}
	}
	noteBlob(c, "blob/full", 10, 100, 1000)

	if got := c.blobs["blob/dup1"].live; got != 100 {
		t.Fatalf("shared member counted as %d live bytes, want 100", got)
	}
	plan := c.plan()
	if !slices.Equal(plan.blobs, []string{"blob/dup1", "blob/dup2"}) {
		t.Fatalf("planned %v", plan.blobs)
	}
	// Every entry has to be repointed, however few members they share.
	if len(plan.entries) != 16 {
		t.Fatalf("plan moves %d entries, want 16", len(plan.entries))
	}
}

func TestBlobConsolidator_NeverSelectsAPartiallyTrackedBlob(t *testing.T) {
	// A cap small enough that tracking every entry is impossible.
	t.Setenv(envConsolidateTrack, "600")
	c := newBlobConsolidator()
	noteBlob(c, "blob/full", 40, 100, 4000)
	noteBlob(c, "blob/thin", 2, 100, 4000)
	noteBlob(c, "blob/thin2", 2, 100, 4000)

	if !c.blobs["blob/full"].partial {
		t.Fatal("the largest candidate list was not the one evicted")
	}
	for _, blob := range c.plan().blobs {
		if c.blobs[blob].partial {
			t.Errorf("planned %s, whose entry list is incomplete", blob)
		}
	}
	// Live accounting survives eviction, so the trigger still sees the blob.
	if c.blobs["blob/full"].live != 4000 {
		t.Errorf("evicted blob reports %d live bytes", c.blobs["blob/full"].live)
	}
}

func TestBlobConsolidator_ClampsAnImplausibleBlobTotal(t *testing.T) {
	// A blob seals at one budget of plaintext, so no blob delivers more than
	// two budgets of stored bytes. Without the clamp a single entry claiming
	// otherwise would make every blob in the repository look sparse, and every
	// backup would spend its whole budget rewriting full ones.
	t.Setenv(envBlobBudget, "1000")
	c := newBlobConsolidator()
	noteBlob(c, "blob/a", 10, 100, 1000)
	noteBlob(c, "blob/b", 10, 100, 1000)
	c.note("k", "id-liar", bodyRef("blob/liar", 0, 10, 1<<62))

	if got := c.full(); got != 2000 {
		t.Fatalf("a full blob measured at %d bytes, want the 2000-byte clamp", got)
	}
	// blob/liar is the only one under the mark, and one blob is no merge.
	if plan := c.plan(); len(plan.blobs) != 0 {
		t.Fatalf("planned %v", plan.blobs)
	}
}

func TestBlobConsolidator_IgnoresUnusableReferences(t *testing.T) {
	c := newBlobConsolidator()
	c.note("k", "id", nil)
	c.note("k", "id", bodyRef("blob/x", 0, 100, 0))   // no denominator
	c.note("k", "id", bodyRef("blob/y", 0, 0, 1000))  // no extent
	c.note("k", "id", bodyRef("blob/z", -1, 10, 100)) // impossible offset
	if len(c.blobs) != 0 {
		t.Fatalf("tracked %v", c.blobs)
	}
	if plan := c.plan(); len(plan.entries) != 0 {
		t.Fatalf("planned %v", plan)
	}
}

// TestShouldConsolidate_RespectsIgnoreEmptySnapshot pins the one contract
// consolidation has to stand down for. Rewriting bodies changes the tree, so a
// caller that asked an unchanged tree to produce no snapshot would get one
// every time — and a run that changed nothing has written no blob for the next
// one to consolidate away either.
func TestShouldConsolidate_RespectsIgnoreEmptySnapshot(t *testing.T) {
	bm := &BackupManager{stats: &backupStats{}}
	if bm.shouldConsolidate() {
		t.Error("consolidated with no accumulator")
	}

	bm.consolidation = newBlobConsolidator()
	if !bm.shouldConsolidate() {
		t.Error("declined an ordinary backup")
	}

	bm.cfg.ignoreEmptySnapshot = true
	if bm.shouldConsolidate() {
		t.Error("consolidated under -ignore-empty with nothing changed")
	}

	bm.stats.filesChanged.Add(1)
	if !bm.shouldConsolidate() {
		t.Error("declined under -ignore-empty although the source changed")
	}
}

// --- end to end through the engine -----------------------------------------

// consolidationRun ages a repository the way real churn does: 48 small files
// packed into three full blobs, then a backup that rewrites 40 of them, which
// leaves every one of those three blobs holding a few live bodies among the
// dead.
//
// Two backups rather than many on purpose. A snapshot's Created time has
// one-second resolution, so a test that takes ten backups in a millisecond
// leaves the catalog unable to say which one is newest, and each backup then
// diffs against an arbitrary predecessor. Two is the largest number of
// backups that is unambiguous without sleeping.
//
// It returns the store and both snapshot refs.
func consolidationRun(t *testing.T) (*MockStore, string, string) {
	t.Helper()
	ctx := context.Background()
	dest := NewMockStore()
	src := NewMockSource()
	for i := range 48 {
		src.AddFile(fmt.Sprintf("f%02d.txt", i), fmt.Sprintf("id-%02d", i), body(int64(i), 1024))
	}

	first, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	if got := len(snapshotBlobs(t, dest, first.SnapshotRef)); got != 3 {
		t.Fatalf("first backup wrote %d blobs, want 3", got)
	}

	for i := range 40 {
		churn(src, i, i)
	}
	last, err := NewBackupManager(v3Deps(dest), src).Run(ctx)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if last.FilesChanged != 40 {
		t.Fatalf("second backup changed %d files, want 40", last.FilesChanged)
	}
	return dest, first.SnapshotRef, last.SnapshotRef
}

// snapshotBlobs is the set of blob objects a snapshot's entries reach, which
// is what a restore of it has to issue requests against.
func snapshotBlobs(t *testing.T, dest *MockStore, snapRef string) []string {
	t.Helper()
	data, err := dest.Get(context.Background(), snapRef)
	if err != nil {
		t.Fatalf("read %s: %v", snapRef, err)
	}
	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode %s: %v", snapRef, err)
	}
	seen := map[string]bool{}
	tree := hamt.NewTree(dest, hamt.WithFormatV3())
	err = tree.WalkReachable(context.Background(), snap.Root, nil,
		func(key, value string, refs hamt.EntryRefs) error {
			if refs.Body != nil {
				seen[refs.Body.Blob] = true
			}
			return nil
		})
	if err != nil {
		t.Fatalf("walk %s: %v", snapRef, err)
	}
	out := make([]string, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	slices.Sort(out)
	return out
}

// restoreZip restores one snapshot and returns its files by path.
func restoreZip(t *testing.T, dest *MockStore, snapRef string) map[string][]byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := NewRestoreManager(v3Deps(dest)).Run(context.Background(), NewZipRestoreWriter(&buf), snapRef); err != nil {
		t.Fatalf("restore %s: %v", snapRef, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open restored zip: %v", err)
	}
	out := map[string][]byte{}
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
		out[f.Name] = data
	}
	return out
}

// TestV3Consolidate_TakesBlobCountOffTheBackupAxis is the measurement the
// change exists for: the same churn, with and without consolidation, compared
// by how many distinct blobs the newest snapshot has to read.
//
// The budget doubles as the off switch — one that cannot admit two blobs plans
// nothing — which is what makes the two runs differ in exactly the thing under
// test and nothing else.
func TestV3Consolidate_TakesBlobCountOffTheBackupAxis(t *testing.T) {
	// 16 KB blobs, so 48 files of 1 KB seal three full ones.
	t.Setenv(envBlobBudget, strconv.Itoa(16<<10))

	t.Setenv(envConsolidateRewrite, "1")
	plain, plainFirst, plainLast := consolidationRun(t)
	before := snapshotBlobs(t, plain, plainLast)

	t.Setenv(envConsolidateRewrite, strconv.Itoa(consolidateRewriteBytes))
	merged, mergedFirst, mergedLast := consolidationRun(t)
	after := snapshotBlobs(t, merged, mergedLast)

	if len(after) >= len(before) {
		t.Fatalf("newest snapshot reads %d blobs with consolidation, %d without", len(after), len(before))
	}
	t.Logf("blobs the newest snapshot reads: %d without consolidation, %d with", len(before), len(after))

	// The correctness bar: every snapshot consolidation has run over restores
	// exactly what it always did.
	if got, want := restoreZip(t, merged, mergedLast), restoreZip(t, plain, plainLast); !sameTree(got, want) {
		t.Error("the consolidated repository restores a different newest snapshot")
	}
	first := restoreZip(t, merged, mergedFirst)
	if len(first) == 0 {
		t.Fatal("the first snapshot restored nothing")
	}
	if want := restoreZip(t, plain, plainFirst); !sameTree(first, want) {
		t.Error("the first snapshot restores differently after consolidation ran over it")
	}

	// And the repository is still internally consistent, bodies included.
	res, err := NewCheckManager(v3Deps(merged)).Run(context.Background(), WithReadData())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("check -read-data after consolidation: %v", res.Errors)
	}
}

// TestV3Consolidate_RetiredBlobsSurviveUntilForgotten pins the half of the
// design prune owns: a blob a newer snapshot has moved off is still reachable
// from the older snapshots that name it, and is collected only once none does.
func TestV3Consolidate_RetiredBlobsSurviveUntilForgotten(t *testing.T) {
	ctx := context.Background()
	t.Setenv(envBlobBudget, strconv.Itoa(16<<10))

	dest, first, last := consolidationRun(t)
	retired := snapshotBlobs(t, dest, first)
	for _, b := range snapshotBlobs(t, dest, last) {
		if slices.Contains(retired, b) {
			t.Fatalf("the newest snapshot still reads %s, which consolidation should have retired", b)
		}
	}
	want := restoreZip(t, dest, first)

	// Pruning with every snapshot retained must not touch a retired blob.
	if _, err := NewPruneManager(v3Deps(dest)).Run(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if got := restoreZip(t, dest, first); !sameTree(got, want) {
		t.Fatal("the first snapshot changed under a prune that retained it")
	}

	// Forgetting the snapshots that named them is what makes the retirement
	// pay: the blobs go, and the newest snapshot is untouched.
	live := restoreZip(t, dest, last)
	blobsBefore := dest.CountPrefix("blob/")
	if _, err := NewForgetManager(v3Deps(dest)).Run(ctx, first); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, err := NewPruneManager(v3Deps(dest)).Run(ctx); err != nil {
		t.Fatalf("prune after forget: %v", err)
	}
	if got := dest.CountPrefix("blob/"); got >= blobsBefore {
		t.Errorf("prune reclaimed nothing: %d blobs before, %d after", blobsBefore, got)
	}
	if got := restoreZip(t, dest, last); !sameTree(got, live) {
		t.Error("the newest snapshot changed after the older ones were collected")
	}
}

func sameTree(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, data := range a {
		if !bytes.Equal(b[name], data) {
			return false
		}
	}
	return true
}
