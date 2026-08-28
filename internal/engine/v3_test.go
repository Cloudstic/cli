package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
)

// v3Deps is the dependency set every manager in these tests is built from:
// one store, format v3.
func v3Deps(dest *MockStore) Deps {
	return Deps{Store: dest, Reporter: ui.NewNoOpReporter(), FormatV3: true}
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
		root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: 4, Inline: []byte("data")})
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
		root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: 8, Inline: []byte("tampered")})
		v3Snapshot(t, dest, root)
		wantFinding(t, run(t, dest, nil, WithReadData()), "corrupt", "reconstructed content")
	})

	t.Run("leaf size disagrees with filemeta", func(t *testing.T) {
		dest := NewMockStore()
		body := []byte("data")
		fm := core.FileMeta{
			FileID: "id-x", Name: "x", Type: core.FileTypeFile,
			ContentHash: core.ComputeHash(body), Size: int64(len(body)),
		}
		ref, data := metaFor(t, fm)
		root := insertV3Entry(t, dest, ref, &hamt.Payload{Meta: data, Size: 999, Inline: body})
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
