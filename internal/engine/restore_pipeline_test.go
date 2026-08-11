package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
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
//
// A Get whose key hold selects does not return until gate is released. That
// lets a test hold a known number of fetches in flight for as long as it
// needs, rather than infer overlap from the order in which a loaded machine
// happened to finish two independent sleeps.
//
// track narrows what the peak counts. A restore fetches metadata concurrently
// as well as content, and since the two now interleave — the walk reads the
// next directory while the current one's files are being written — a peak over
// every key cannot tell the two apart.
type blockingStore struct {
	store.ObjectStore
	delay    time.Duration
	hold     func(key string) bool
	track    func(key string) bool
	gate     *fetchGate
	inFlight atomic.Int64
	peak     atomic.Int64
}

func (s *blockingStore) Get(ctx context.Context, key string) ([]byte, error) {
	counted := s.track == nil || s.track(key)
	if counted {
		n := s.inFlight.Add(1)
		for {
			p := s.peak.Load()
			if n <= p || s.peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer s.inFlight.Add(-1)
	}

	if s.hold != nil && s.hold(key) {
		if err := s.gate.wait(ctx); err != nil {
			return nil, err
		}
	} else if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.ObjectStore.Get(ctx, key)
}

// fetchGate holds blockingStore fetches until something releases it.
// Releasing is idempotent, because both the writer that is waiting for the
// held fetches and the watchdog that gives up on them may call it.
type fetchGate struct {
	ch   chan struct{}
	once sync.Once
}

func newFetchGate() *fetchGate { return &fetchGate{ch: make(chan struct{})} }

func (g *fetchGate) release() { g.once.Do(func() { close(g.ch) }) }

func (g *fetchGate) released() bool {
	select {
	case <-g.ch:
		return true
	default:
		return false
	}
}

func (g *fetchGate) wait(ctx context.Context) error {
	select {
	case <-g.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handshakeTimeout bounds the wait for the pipeline to reach the state under
// test. It only has to outlast a scheduling hiccup: a pipelined writeChunks
// satisfies the handshake on its first write, and an implementation that
// cannot satisfy it will not start to no matter how long the test waits.
const handshakeTimeout = 5 * time.Second

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

	// The discriminating measurement is what the store is doing *during* a
	// write, not the peak. A batched implementation also reaches a high peak —
	// it fires a whole batch at once — but then blocks on g.Wait() and writes
	// with nothing outstanding.
	//
	// So only the first chunk's fetch is allowed to complete on its own; every
	// other fetch hangs on the gate. A pipeline writes chunk 0 in that state,
	// with the rest of its window still outstanding, which is exactly the
	// property under test. A batched implementation cannot write anything
	// until its whole batch has landed, so it never reaches Write at all and
	// the watchdog below has to open the gate on its behalf.
	bs := &blockingStore{
		ObjectStore: inner,
		gate:        newFetchGate(),
		hold:        func(key string) bool { return key != refs[0] },
	}
	rm := NewRestoreManager(Deps{Store: bs, Reporter: ui.NewNoOpReporter()})

	const wantInFlight = 2
	w := &handshakeWriter{inFlight: &bs.inFlight, gate: bs.gate, want: wantInFlight}

	// Without this, a regression to fetch-all-then-write-all would hang the
	// test instead of failing it: every held fetch is waiting for a gate that
	// only the first Write opens, and that Write never comes.
	watchdog := time.AfterFunc(handshakeTimeout, bs.gate.release)
	defer watchdog.Stop()

	if err := rm.writeChunks(ctx, w, refs, 1024); err != nil {
		t.Fatalf("writeChunks: %v", err)
	}

	if !bytes.Equal(w.buf.Bytes(), want.Bytes()) {
		t.Fatal("pipelined writeChunks did not reconstruct the original byte sequence")
	}
	if peak := bs.peak.Load(); peak < 2 {
		t.Errorf("chunk fetches never overlapped (peak in-flight = %d)", peak)
	}
	if w.observed < wantInFlight {
		t.Errorf("only %d fetches were in flight when the first chunk was written, want at least %d; the chunk pipeline has regressed to fetch-all-then-write-all", w.observed, wantInFlight)
	}
}

// handshakeWriter blocks its first Write until it sees want store fetches
// outstanding, then releases them and records what it saw. Waiting for the
// condition is what makes the measurement independent of scheduling: sampling
// it, as this test used to, reports whichever way two unrelated sleeps
// happened to interleave — on a loaded machine every fetch's sleep can elapse
// before the writer is scheduled at all, draining the window the test is
// there to observe.
type handshakeWriter struct {
	inFlight *atomic.Int64
	gate     *fetchGate
	want     int64

	// Written from Write, which writeChunks calls on its caller's goroutine,
	// so the test reads these after it returns without synchronisation.
	buf      bytes.Buffer
	observed int64

	first sync.Once
}

func (w *handshakeWriter) Write(p []byte) (int, error) {
	w.first.Do(func() {
		w.observed = w.awaitFetches()
		w.gate.release()
	})
	return w.buf.Write(p)
}

// awaitFetches waits for at least w.want fetches to be outstanding and returns
// the count it saw. It gives up once the gate has been released, since the
// fetches it is waiting for are then free to finish and any count it reports
// would be meaningless — that is the path a batched implementation takes, and
// what makes the reported count fail the assertion above.
func (w *handshakeWriter) awaitFetches() int64 {
	for {
		if n := w.inFlight.Load(); n >= w.want {
			return n
		}
		if w.gate.released() {
			return w.inFlight.Load()
		}
		time.Sleep(200 * time.Microsecond)
	}
}

// TestRunWithWriter_RestoresFilesConcurrently guards the other half: files
// used to be restored strictly one after another, so a snapshot of many small
// files cost one full round trip each no matter how much the store could take.
func TestRunWithWriter_RestoresFilesConcurrently(t *testing.T) {
	ctx := context.Background()

	// Several files under one directory, so the overlap under test is between
	// file writes rather than between one write and the walk that follows it.
	src := NewMockSource()
	for i := range 8 {
		src.AddFile(fmt.Sprintf("file%d.txt", i), fmt.Sprintf("id%d", i), []byte("content"))
	}
	dest := NewMockStore()
	if _, err := NewBackupManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()}, src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Only content reads are counted. Metadata is fetched concurrently in its
	// own right, and counting that would make this pass even with a strictly
	// sequential file loop — the exact regression it exists to catch.
	bs := &blockingStore{
		ObjectStore: dest,
		delay:       5 * time.Millisecond,
		track:       func(key string) bool { return strings.HasPrefix(key, "content/") },
	}
	rm := NewRestoreManager(Deps{Store: bs, Reporter: ui.NewNoOpReporter()})

	writer, err := NewFSRestoreWriter(t.TempDir())
	if err != nil {
		t.Fatalf("writer: %v", err)
	}

	res, err := rm.Run(ctx, writer, "")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Errors != 0 {
		t.Fatalf("restore reported %d errors", res.Errors)
	}
	if res.FilesWritten != 8 {
		t.Fatalf("restored %d files, want 8", res.FilesWritten)
	}
	if peak := bs.peak.Load(); peak < 2 {
		t.Errorf("restore issued one content request at a time (peak in-flight = %d); file-level restore has become sequential again", peak)
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
