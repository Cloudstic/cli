package cloudstic

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/cloudstic/cli/pkg/keychain"
	localsource "github.com/cloudstic/cli/pkg/source/local"
	"github.com/cloudstic/cli/pkg/store"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// countingStore records the operation mix a run performs.
//
// Wall-clock time on a local disk is the wrong measure for this feature: a copy
// between real repositories is bound by round trips to two backends, so what
// has to stay bounded is the *number* of Gets, Puts and Exists calls, not how
// fast a laptop can service them.
type countingStore struct {
	store.ObjectStore
	gets, puts, exists, lists atomic.Int64
	bytesGet, bytesPut        atomic.Int64
}

func newCountingStore(inner store.ObjectStore) *countingStore {
	return &countingStore{ObjectStore: inner}
}

func (c *countingStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := c.ObjectStore.Get(ctx, key)
	c.gets.Add(1)
	c.bytesGet.Add(int64(len(data)))
	return data, err
}

func (c *countingStore) Put(ctx context.Context, key string, data []byte) error {
	c.puts.Add(1)
	c.bytesPut.Add(int64(len(data)))
	return c.ObjectStore.Put(ctx, key, data)
}

func (c *countingStore) Exists(ctx context.Context, key string) (bool, error) {
	c.exists.Add(1)
	return c.ObjectStore.Exists(ctx, key)
}

func (c *countingStore) List(ctx context.Context, prefix string) ([]string, error) {
	c.lists.Add(1)
	return c.ObjectStore.List(ctx, prefix)
}

func (c *countingStore) reset() {
	c.gets.Store(0)
	c.puts.Store(0)
	c.exists.Store(0)
	c.lists.Store(0)
	c.bytesGet.Store(0)
	c.bytesPut.Store(0)
}

func (c *countingStore) String() string {
	return fmt.Sprintf("get=%d put=%d exists=%d list=%d readB=%d writeB=%d",
		c.gets.Load(), c.puts.Load(), c.exists.Load(), c.lists.Load(),
		c.bytesGet.Load(), c.bytesPut.Load())
}

// countedRepo opens a repository whose backend operations are counted.
func countedRepo(tb testing.TB, dir, password string) (*Client, *countingStore) {
	tb.Helper()
	ctx := context.Background()

	backend, err := localstore.New(dir)
	if err != nil {
		tb.Fatalf("open store: %v", err)
	}
	counted := newCountingStore(backend)

	initOpts := []InitOption{WithInitNoEncryption()}
	clientOpts := []ClientOption{}
	if password != "" {
		chain := keychain.Chain{keychain.WithPassword(password)}
		initOpts = []InitOption{WithInitCredentials(chain)}
		clientOpts = append(clientOpts, WithKeychain(chain))
	}
	if _, err := InitRepo(ctx, counted, initOpts...); err != nil {
		tb.Fatalf("init: %v", err)
	}
	client, err := NewClient(ctx, counted, clientOpts...)
	if err != nil {
		tb.Fatalf("open client: %v", err)
	}
	return client, counted
}

// writeCorpus lays out a directory of files with deterministic pseudo-random
// content, large enough that chunking and encryption actually do work.
func writeCorpus(tb testing.TB, dir string, files, sizeEach int) {
	tb.Helper()
	rng := rand.New(rand.NewSource(1))
	buf := make([]byte, sizeEach)
	for i := range files {
		if _, err := rng.Read(buf); err != nil {
			tb.Fatalf("rand: %v", err)
		}
		path := filepath.Join(dir, fmt.Sprintf("sub%02d", i%8), fmt.Sprintf("file%04d.bin", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			tb.Fatalf("write: %v", err)
		}
	}
}

// touchOne rewrites a single file, so the next snapshot differs from the last
// by exactly one file.
func touchOne(tb testing.TB, dir string, n int) {
	tb.Helper()
	path := filepath.Join(dir, fmt.Sprintf("sub%02d", n%8), fmt.Sprintf("file%04d.bin", n))
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read: %v", err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(path, data, 0o644); err != nil {
		tb.Fatalf("write: %v", err)
	}
}

// TestCopyScalesWithRepositoryNotSnapshotCount is the measurement behind the
// per-run remap table (RFC 0017 §4.2).
//
// Consecutive snapshots of one source share nearly all of their graph. Without
// a table spanning the whole run, every unchanged file would be re-read and
// re-encrypted once per snapshot, and the cost of copying a history would be
// the size of the repository multiplied by the number of snapshots in it. This
// is the property that would regress silently, so it is asserted rather than
// merely reported.
func TestCopyScalesWithRepositoryNotSnapshotCount(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a multi-file corpus")
	}
	ctx := context.Background()

	const files = 40
	// BytesRead is the manager's own count of plaintext pulled through the
	// source, which is precisely what the remap table governs. Backend
	// operation counts are the wrong probe here: PackStore bundles small
	// objects, so its cache would absorb repeated reads and make a missing
	// table look free.
	measure := func(snapshots int) (plaintextRead int64, dstPuts int64) {
		srcDir, dstDir, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
		writeCorpus(t, dataDir, files, 4096)

		src, srcCount := countedRepo(t, srcDir, "src-pw")
		dst, _ := countedRepo(t, dstDir, "dst-pw")

		for i := range snapshots {
			if i > 0 {
				touchOne(t, dataDir, i)
			}
			if _, err := src.Backup(ctx, localsource.New(dataDir)); err != nil {
				t.Fatalf("backup %d: %v", i, err)
			}
		}

		srcCount.reset()
		dstCounted := dst.base.(*countingStore)
		dstCounted.reset()

		res, err := dst.CopyFrom(ctx, src)
		if err != nil {
			t.Fatalf("CopyFrom: %v", err)
		}
		if len(res.Copied) != snapshots {
			t.Fatalf("copied %d snapshots, want %d", len(res.Copied), snapshots)
		}
		t.Logf("%2d snapshots: plaintext read %d B, written %d B", snapshots, res.BytesRead, res.BytesWritten)
		t.Logf("%2d snapshots: source backend %s", snapshots, srcCount)
		t.Logf("%2d snapshots: dest   backend %s", snapshots, dstCounted)
		return res.BytesRead, dstCounted.puts.Load()
	}

	oneRead, onePuts := measure(1)
	manyRead, manyPuts := measure(8)

	// Eight snapshots differing by one file each must not cost eight times one
	// snapshot. Allow generous headroom for per-snapshot overhead (the snapshot
	// object, the changed file's chunks, the rewritten HAMT spine and the
	// catalog) while still failing loudly if the per-file work is repeated.
	if manyRead > oneRead*3 {
		t.Errorf("plaintext read scaled with snapshot count: %d B for 1 snapshot, %d B for 8"+
			" (expected well under %d; the remap table is not spanning the run)",
			oneRead, manyRead, oneRead*3)
	}
	if manyPuts > onePuts*3 {
		t.Errorf("destination writes scaled with snapshot count: %d for 1 snapshot, %d for 8"+
			" (expected well under %d; unchanged subtrees are not being shared)",
			onePuts, manyPuts, onePuts*3)
	}
}

// BenchmarkCopy measures a copy of a single snapshot against the backup that
// produced it, so the overhead of re-addressing a graph is expressed against
// the cost of writing it in the first place.
func BenchmarkCopy(b *testing.B) {
	for _, size := range []struct {
		name            string
		files, sizeEach int
	}{
		{"40x4KiB", 40, 4096},
		{"20x1MiB", 20, 1 << 20},
	} {
		b.Run(size.name, func(b *testing.B) {
			ctx := context.Background()
			for b.Loop() {
				b.StopTimer()
				srcDir, dstDir, dataDir := b.TempDir(), b.TempDir(), b.TempDir()
				writeCorpus(b, dataDir, size.files, size.sizeEach)
				src, _ := countedRepo(b, srcDir, "src-pw")
				dst, dstCount := countedRepo(b, dstDir, "dst-pw")
				if _, err := src.Backup(ctx, localsource.New(dataDir)); err != nil {
					b.Fatalf("backup: %v", err)
				}
				dstCount.reset()
				b.StartTimer()

				if _, err := dst.CopyFrom(ctx, src); err != nil {
					b.Fatalf("CopyFrom: %v", err)
				}
			}
		})
	}
}

// BenchmarkCopyRerun measures the cost of a copy that has nothing to do, which
// is what a scheduled copy does almost every time it runs.
func BenchmarkCopyRerun(b *testing.B) {
	ctx := context.Background()
	srcDir, dstDir, dataDir := b.TempDir(), b.TempDir(), b.TempDir()
	writeCorpus(b, dataDir, 40, 4096)
	src, _ := countedRepo(b, srcDir, "src-pw")
	dst, _ := countedRepo(b, dstDir, "dst-pw")
	for i := range 8 {
		if i > 0 {
			touchOne(b, dataDir, i)
		}
		if _, err := src.Backup(ctx, localsource.New(dataDir)); err != nil {
			b.Fatalf("backup: %v", err)
		}
	}
	if _, err := dst.CopyFrom(ctx, src); err != nil {
		b.Fatalf("seed copy: %v", err)
	}

	b.ResetTimer()
	for b.Loop() {
		res, err := dst.CopyFrom(ctx, src)
		if err != nil {
			b.Fatalf("CopyFrom: %v", err)
		}
		if len(res.Copied) != 0 {
			b.Fatalf("rerun copied %d snapshots, want 0", len(res.Copied))
		}
	}
}

// TestScheduledCopyIsIncrementalAcrossRuns covers the shape a scheduled copy
// actually has: the destination is already current, one new snapshot appears,
// and a fresh process brings it over.
//
// Each run starts with an empty remap table, so without recovering the tree
// pairing from provenance the first snapshot of every run rebuilds a whole tree
// — a cost proportional to the repository, paid daily, to record a day's
// changes. Measured on a 2000-file tree that was 706 KB of plaintext read per
// run against 1.6 KB once the pairing is recovered.
func TestScheduledCopyIsIncrementalAcrossRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a multi-file corpus")
	}
	ctx := context.Background()

	srcDir, dstDir, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
	const files = 400
	writeCorpus(t, dataDir, files, 512)
	src, _ := countedRepo(t, srcDir, "src-pw")
	dst, _ := countedRepo(t, dstDir, "dst-pw")

	for i := range 3 {
		if i > 0 {
			touchOne(t, dataDir, i)
		}
		if _, err := src.Backup(ctx, localsource.New(dataDir)); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
	}
	seed, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("seed copy: %v", err)
	}
	if len(seed.Copied) != 3 {
		t.Fatalf("seed copy took %d snapshots, want 3", len(seed.Copied))
	}

	// One new snapshot, then a run that has no in-process state to lean on.
	touchOne(t, dataDir, 7)
	if _, err := src.Backup(ctx, localsource.New(dataDir)); err != nil {
		t.Fatalf("incremental backup: %v", err)
	}

	catchUp, err := dst.CopyFrom(ctx, src)
	if err != nil {
		t.Fatalf("catch-up copy: %v", err)
	}
	if len(catchUp.Copied) != 1 {
		t.Fatalf("catch-up copied %d snapshots, want 1", len(catchUp.Copied))
	}

	t.Logf("catch-up over a %d-file tree read %d B (seed run read %d B)",
		files, catchUp.BytesRead, seed.BytesRead)

	// The catch-up brought over one changed file. Reading anything close to what
	// the seed run read means the tree was rebuilt rather than diffed.
	if catchUp.BytesRead > seed.BytesRead/10 {
		t.Errorf("catch-up read %d B against a seed run of %d B: a run that adds one"+
			" snapshot is rebuilding the whole tree instead of applying a diff",
			catchUp.BytesRead, seed.BytesRead)
	}
}
