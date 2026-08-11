package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"testing"

	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
)

// TestRestoreManager_WriteChunks_PreservesOrder verifies that fetching
// chunks concurrently in batches still reconstructs the exact original byte
// sequence, regardless of which fetch within a batch completes first.
func TestRestoreManager_WriteChunks_PreservesOrder(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()
	rm := NewRestoreManager(Deps{Store: s, Reporter: ui.NewNoOpReporter()})

	const n = 37 // more than one batch at the default concurrency hint (10)
	refs := make([]string, n)
	var want bytes.Buffer
	for i := 0; i < n; i++ {
		data := bytes.Repeat([]byte{byte(i)}, 100)
		ref := fmt.Sprintf("chunk/%02d", i)
		if err := s.Put(ctx, ref, data); err != nil {
			t.Fatalf("Put %s: %v", ref, err)
		}
		refs[i] = ref
		want.Write(data)
	}

	var got bytes.Buffer
	if err := rm.writeChunks(ctx, &got, refs, 100); err != nil {
		t.Fatalf("writeChunks: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Error("writeChunks did not reconstruct the original byte sequence in order")
	}
}

// TestRestoreManager_WriteChunks_FetchesConcurrently confirms chunks within a
// batch are actually fetched in parallel rather than one at a time.
func TestRestoreManager_WriteChunks_FetchesConcurrently(t *testing.T) {
	ctx := context.Background()
	inner := NewMockStore()
	tracker := &concurrencyTrackingStore{ObjectStore: inner}
	rm := NewRestoreManager(Deps{Store: tracker, Reporter: ui.NewNoOpReporter()})

	const n = 8
	refs := make([]string, n)
	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("chunk/%02d", i)
		if err := inner.Put(ctx, ref, []byte{byte(i)}); err != nil {
			t.Fatalf("Put %s: %v", ref, err)
		}
		refs[i] = ref
	}

	var buf bytes.Buffer
	if err := rm.writeChunks(ctx, &buf, refs, 1); err != nil {
		t.Fatalf("writeChunks: %v", err)
	}

	if peak := tracker.peakConcurrency(); peak < 2 {
		t.Errorf("expected concurrent chunk fetches (peak > 1), got peak=%d", peak)
	}
}

// TestRestoreManager_CollectMetadata_FetchesConcurrently confirms
// collectMetadata fetches filemeta objects concurrently (mirroring
// fetchSnapshots' pattern) instead of one blocking Get at a time.
func TestRestoreManager_CollectMetadata_FetchesConcurrently(t *testing.T) {
	ctx := context.Background()
	dest := setupBackupForRestore(t)

	tracker := &concurrencyTrackingStore{ObjectStore: dest}
	rsMgr := NewRestoreManager(Deps{Store: tracker, Reporter: ui.NewNoOpReporter()})

	snap, _, err := rsMgr.resolveSnapshot(ctx, "")
	if err != nil {
		t.Fatalf("resolveSnapshot: %v", err)
	}

	byID, walkOrder, routing, err := rsMgr.collectMetadata(ctx, snap.Root)
	if err != nil {
		t.Fatalf("collectMetadata: %v", err)
	}
	if len(byID) == 0 {
		t.Fatal("expected at least one file/dir in collected metadata")
	}
	// The routing prefix is what tells restoreUnreached whether the derived
	// walk would have found an entry, so a gap in it is a silently unrestorable
	// entry rather than a cosmetic one.
	for id := range byID {
		if len(routing[id]) != derivedPrefixLen {
			t.Errorf("entry %s has routing prefix %q, want %d hex characters", id, routing[id], derivedPrefixLen)
		}
	}
	// Walk order is what the pack-locality ordering rests on, so it must be
	// complete and free of the gaps a concurrent fetch would leave if results
	// were appended as they landed rather than placed by index.
	if len(walkOrder) != len(byID) {
		t.Errorf("walk order has %d entries, byID has %d", len(walkOrder), len(byID))
	}
	for i, id := range walkOrder {
		if id == "" {
			t.Fatalf("walk order has a gap at index %d", i)
		}
	}

	if peak := tracker.peakConcurrency(); peak < 2 {
		t.Errorf("expected concurrent metadata fetches (peak > 1), got peak=%d", peak)
	}
}

// TestRestoreManager_MultiChunkFile_RoundTrips backs up a file large enough
// to split into several content-defined chunks, then restores it through a
// concurrency-tracking store: this is the end-to-end check that parallel
// chunk fetching is both used and byte-correct on a real multi-chunk file,
// not just against directly-constructed chunk refs.
func TestRestoreManager_MultiChunkFile_RoundTrips(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()

	// Random content well above FastCDC's 512KB minimum chunk size, so the
	// file reliably splits into multiple chunks (average target is 1MB).
	content := make([]byte, 6*1024*1024)
	if _, err := rand.Read(content); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	src.AddFile("big.bin", "id1", content)

	raw := NewMockStore()
	bkMgr := NewBackupManager(Deps{Store: raw, Reporter: ui.NewNoOpReporter()}, src)
	if _, err := bkMgr.Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}

	tracker := &concurrencyTrackingStore{ObjectStore: storelayer.NewCompressedStore(raw)}
	rsMgr := NewRestoreManager(Deps{Store: tracker, Reporter: ui.NewNoOpReporter()})

	var buf bytes.Buffer
	if _, err := rsMgr.Run(ctx, NewZipRestoreWriter(&buf), ""); err != nil {
		t.Fatalf("restore: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	var got []byte
	for _, f := range zr.File {
		if f.Name != "big.bin" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry: %v", err)
		}
		got, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read zip entry: %v", err)
		}
	}
	if !bytes.Equal(got, content) {
		t.Fatal("restored content does not match original — chunk order or data corrupted")
	}

	if peak := tracker.peakConcurrency(); peak < 2 {
		t.Errorf("expected concurrent chunk fetches during restore, peak concurrency = %d", peak)
	}
}
