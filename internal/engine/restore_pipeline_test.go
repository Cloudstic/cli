package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// blockingStore makes every Get take a fixed amount of time, which is what a
// remote backend's round trip looks like to the restore path, and records how
// many Gets are outstanding at once.
type blockingStore struct {
	store.ObjectStore
	delay    time.Duration
	inFlight atomic.Int64
	peak     atomic.Int64
}

func (s *blockingStore) Get(ctx context.Context, key string) ([]byte, error) {
	n := s.inFlight.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer s.inFlight.Add(-1)
	time.Sleep(s.delay)
	return s.ObjectStore.Get(ctx, key)
}

// TestWriteChunks_KeepsFetchesInFlightWhileWriting is the regression guard for
// the batch barrier this replaced. The old shape — fetch N chunks, wait for
// every one, then write all N — left the store completely idle for the whole
// write phase of each batch. Asserting that concurrency never collapses to a
// single outstanding Get is what distinguishes a pipeline from a batch.
func TestWriteChunks_KeepsFetchesInFlightWhileWriting(t *testing.T) {
	ctx := context.Background()
	inner := NewMockStore()

	const n = 40
	refs := make([]string, n)
	var want bytes.Buffer
	for i := range refs {
		data := bytes.Repeat([]byte{byte(i)}, 1024)
		ref := fmt.Sprintf("chunk/%02d", i)
		if err := inner.Put(ctx, ref, data); err != nil {
			t.Fatalf("Put %s: %v", ref, err)
		}
		refs[i] = ref
		want.Write(data)
	}

	bs := &blockingStore{ObjectStore: inner, delay: 2 * time.Millisecond}
	rm := NewRestoreManager(bs, ui.NewNoOpReporter())

	// The discriminating measurement is what the store is doing *during* a
	// write, not the peak. A batched implementation also reaches a high peak —
	// it fires a whole batch at once — but then blocks on g.Wait() and writes
	// with nothing outstanding, so every write observes zero fetches in
	// flight. A pipeline keeps the next chunks coming while this one is
	// written, so writes observe a non-zero count.
	slow := &slowWriter{delay: time.Millisecond, inFlight: &bs.inFlight}
	if err := rm.writeChunks(ctx, slow, refs, 1024); err != nil {
		t.Fatalf("writeChunks: %v", err)
	}

	if !bytes.Equal(slow.buf.Bytes(), want.Bytes()) {
		t.Fatal("pipelined writeChunks did not reconstruct the original byte sequence")
	}
	if peak := bs.peak.Load(); peak < 2 {
		t.Errorf("chunk fetches never overlapped (peak in-flight = %d)", peak)
	}

	// The final chunks legitimately drain the window, so require a clear
	// majority rather than every write. A batch barrier scores exactly 0.
	overlapped, total := slow.overlapped, slow.writes
	if overlapped*2 < total {
		t.Errorf("only %d of %d writes overlapped an in-flight fetch; the chunk pipeline has regressed to fetch-all-then-write-all", overlapped, total)
	}
}

// slowWriter takes measurable time per Write and samples how many store
// fetches were outstanding while each one ran.
type slowWriter struct {
	delay      time.Duration
	inFlight   *atomic.Int64
	buf        bytes.Buffer
	writes     int
	overlapped int
}

func (w *slowWriter) Write(p []byte) (int, error) {
	if w.inFlight.Load() > 0 {
		w.overlapped++
	}
	w.writes++
	time.Sleep(w.delay)
	return w.buf.Write(p)
}

// TestRunWithWriter_RestoresFilesConcurrently guards the other half: files
// used to be restored strictly one after another, so a snapshot of many small
// files cost one full round trip each no matter how much the store could take.
func TestRunWithWriter_RestoresFilesConcurrently(t *testing.T) {
	ctx := context.Background()
	dest := setupBackupForRestore(t)

	bs := &blockingStore{ObjectStore: dest, delay: 5 * time.Millisecond}
	rm := NewRestoreManager(bs, ui.NewNoOpReporter())

	// Plan and write are driven separately so the peak can be reset in
	// between. prepareRestore fetches file metadata concurrently on its own,
	// and counting that would make this pass even with a strictly sequential
	// file loop — which is the exact regression it exists to catch.
	plan, err := rm.prepareRestore(ctx, "")
	if err != nil {
		t.Fatalf("prepareRestore: %v", err)
	}
	files := 0
	for _, m := range plan.sorted {
		if m.Type != core.FileTypeFolder && m.ContentHash != "" {
			files++
		}
	}
	if files < 2 {
		t.Skipf("fixture has %d files; need at least 2 to observe overlap", files)
	}

	writer, err := NewFSRestoreWriter(t.TempDir())
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	bs.peak.Store(0)

	res, err := rm.runWithWriter(ctx, plan, writer)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Errors != 0 {
		t.Fatalf("restore reported %d errors", res.Errors)
	}
	if peak := bs.peak.Load(); peak < 2 {
		t.Errorf("restore issued one request at a time (peak in-flight = %d); file-level restore has become sequential again", peak)
	}
}

// TestRunWithWriter_ZipStaysSequential is the counterpart guard. A zip is a
// single stream with one entry open at a time, so zipRestoreWriter must not be
// opted into concurrent writes — a directory header landing inside an open
// file entry produces an archive that will not open.
func TestRunWithWriter_ZipStaysSequential(t *testing.T) {
	var buf bytes.Buffer
	w := NewZipRestoreWriter(&buf)
	if cw, ok := w.(concurrentRestoreWriter); ok && cw.SupportsConcurrentWrites() {
		t.Fatal("zipRestoreWriter must not declare support for concurrent writes")
	}
}

// TestFSRestoreWriter_ConcurrentWriteFileIsSafe exercises the writer's shared
// bookkeeping directly, so the locking is covered even when the restore path
// above happens not to fan out.
func TestFSRestoreWriter_ConcurrentWriteFileIsSafe(t *testing.T) {
	w, err := NewFSRestoreWriter(t.TempDir())
	if err != nil {
		t.Fatalf("writer: %v", err)
	}

	const n = 32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			meta := core.FileMeta{FileID: fmt.Sprint(i), Type: core.FileTypeFile}
			body := []byte("payload")
			if err := w.WriteFile(fmt.Sprintf("f%d.txt", i), meta, func(out io.Writer) error {
				_, werr := out.Write(body)
				return werr
			}); err != nil {
				t.Errorf("WriteFile: %v", err)
			}
		}()
	}
	wg.Wait()

	if got, want := w.BytesWritten(), int64(n*len("payload")); got != want {
		t.Errorf("BytesWritten = %d, want %d (a lost update means the counter is unsynchronised)", got, want)
	}
}
