package hamt

// Affinity model benchmark — demonstrates the node-write reduction introduced by
// locality-preserving HAMT keys (RFC 0002).
//
// The core claim: an incremental backup that modifies N files in the same directory
// rewrites O(N·depth) internal nodes with legacy (random) keys, but only O(depth)
// shared path nodes plus leaf-level changes with affinity keys.
//
// Simulation trick: AffinityKey(fileID, fileID) == SHA256(fileID)[:4]+SHA256(fileID)[4:]
// == SHA256(fileID) == computePathKey(fileID), the exact pre-RFC-0002 behavior.
// So passing parentID=fileID reproduces legacy routing without any code change.

import (
	"fmt"
	"testing"
)

// affinityParentFn returns the real directory ID — enables locality-preserving routing.
// All siblings share SHA256("dir-XX")[:4] as their top routing prefix.
func affinityParentFn(dirID, _ string) string { return dirID }

// legacyParentFn simulates pre-affinity computePathKey behavior.
// AffinityKey(fileID, fileID) = SHA256(fileID) = old computePathKey(fileID).
// Every file gets a statistically independent routing key → no locality.
func legacyParentFn(_, fileID string) string { return fileID }

// buildTree inserts nDirs*filesPerDir entries using parentFn to derive each file's parentID.
func buildTree(tb testing.TB, tree *Tree, nDirs, filesPerDir int, parentFn func(dirID, fileID string) string) string {
	tb.Helper()
	root := ""
	var err error
	for d := 0; d < nDirs; d++ {
		dirID := fmt.Sprintf("dir-%02d", d)
		for f := 0; f < filesPerDir; f++ {
			fileID := fmt.Sprintf("file-%04d", d*filesPerDir+f)
			root, err = tree.Insert(root, parentFn(dirID, fileID), fileID, "ref-"+fileID)
			if err != nil {
				tb.Fatalf("Insert dir=%s file=%s: %v", dirID, fileID, err)
			}
		}
	}
	return root
}

// TestAffinityNodeWriteReduction is the primary proof-of-concept for RFC 0002.
//
// It builds a 1 000-file tree (10 directories × 100 files), then runs a simulated
// incremental backup that updates every file in one directory.  The number of new
// nodes written to the persistent store (via FlushReachable) is recorded for both
// key strategies and the test asserts — and reports — the reduction.
//
// Expected output (approximate, varies by hash values):
//
//	affinity keys: ~15–25 node writes
//	legacy keys:   ~50–90 node writes
//
// Why the affinity count is ~20:
// AffinityKey("dir-00", fileID) = SHA256("dir-00")[:4] + SHA256(fileID)[4:].
// Routing consumes 5 bits/level from the first 32 bits of the key:
//   - Levels 0–2 (bits 31–17) come entirely from SHA256("dir-00")[:4].
//     → all 100 dir-00 files share the same L0/L1/L2 path.
//   - Level 3 (bits 16–12): bit 16 from parent, bits 15–12 from file hash.
//     → files diverge here across ~16 occupied L3 leaf buckets.
//
// The incremental update rewrites: 1 root + 3 internal path nodes + ~16 L3 leaves ≈ 20.
//
// Why the legacy count is ~68:
// SHA256(fileID) distributes the 100 updates across ~31 of the 32 L0 buckets.
// Each hit bucket requires its own path update; some buckets are 3 levels deep
// (>32 entries trigger a split), so per-bucket cost is 1–3 nodes plus the shared root.
// Total ≈ 68, not 150–300: FlushReachable writes only the final reachable set,
// not every intermediate node produced during the 100 sequential inserts.
func TestAffinityNodeWriteReduction(t *testing.T) {
	const (
		nDirs       = 10
		filesPerDir = 100
		targetDir   = "dir-00" // the directory whose files will be updated
	)

	type result struct{ puts int }

	measure := func(name string, parentFn func(string, string) string) result {
		// Phase 1: initial backup.
		persistent := newCountingStore()
		ts := NewTransactionalStore(persistent)
		tree := NewTree(ts)

		root := buildTree(t, tree, nDirs, filesPerDir, parentFn)
		if err := ts.FlushReachable(root); err != nil {
			t.Fatalf("%s FlushReachable (initial): %v", name, err)
		}

		// Phase 2: incremental backup — update all filesPerDir files in targetDir.
		// A fresh TransactionalStore gives clean staging while reusing the same
		// persistent data (the already-flushed initial tree).
		persistent.reset()
		ts2 := NewTransactionalStore(persistent)
		tree2 := NewTree(ts2)

		var err error
		for f := 0; f < filesPerDir; f++ {
			// dir-00 owns file-0000 … file-0099.
			fileID := fmt.Sprintf("file-%04d", f)
			root, err = tree2.Insert(root, parentFn(targetDir, fileID), fileID, fmt.Sprintf("ref-%s-v2", fileID))
			if err != nil {
				t.Fatalf("%s Insert (incremental): %v", name, err)
			}
		}
		if err := ts2.FlushReachable(root); err != nil {
			t.Fatalf("%s FlushReachable (incremental): %v", name, err)
		}

		return result{puts: persistent.puts}
	}

	affinity := measure("affinity", affinityParentFn)
	legacy := measure("legacy", legacyParentFn)

	t.Logf("Incremental update of %d files in one directory (%d total files, %d dirs):",
		filesPerDir, nDirs*filesPerDir, nDirs)
	t.Logf("  affinity keys : %4d node writes", affinity.puts)
	t.Logf("  legacy keys   : %4d node writes", legacy.puts)
	t.Logf("  reduction     : %.1f%%  (%d fewer writes)",
		float64(legacy.puts-affinity.puts)/float64(legacy.puts)*100,
		legacy.puts-affinity.puts)

	if affinity.puts >= legacy.puts {
		t.Errorf("expected affinity (%d) < legacy (%d) node writes — locality guarantee violated",
			affinity.puts, legacy.puts)
	}
}

// BenchmarkIncrementalUpdate_Affinity and BenchmarkIncrementalUpdate_Legacy
// measure wall-clock time for a 100-file incremental update against a 1 000-file
// pre-built tree.  Run with:
//
//	go test ./internal/hamt/ -run=^$ -bench=BenchmarkIncrementalUpdate -benchmem
func BenchmarkIncrementalUpdate_Affinity(b *testing.B) {
	benchmarkIncrementalUpdate(b, affinityParentFn)
}

func BenchmarkIncrementalUpdate_Legacy(b *testing.B) {
	benchmarkIncrementalUpdate(b, legacyParentFn)
}

func benchmarkIncrementalUpdate(b *testing.B, parentFn func(string, string) string) {
	b.Helper()
	const (
		nDirs       = 10
		filesPerDir = 100
		targetDir   = "dir-00"
	)

	// Build the initial 1 000-file tree once; this cost is excluded from the timer.
	// countingStore is used (not inMemoryStore) because writeParallel issues concurrent
	// Puts and inMemoryStore has no mutex.
	persistent := newCountingStore()
	ts := NewTransactionalStore(persistent)
	tree := NewTree(ts)
	initialRoot := buildTree(b, tree, nDirs, filesPerDir, parentFn)
	if err := ts.FlushReachable(initialRoot); err != nil {
		b.Fatalf("FlushReachable (setup): %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Fresh staging; reads fall through to the persistent initial tree.
		ts2 := NewTransactionalStore(persistent)
		tree2 := NewTree(ts2)
		root := initialRoot
		b.StartTimer()

		var err error
		for f := 0; f < filesPerDir; f++ {
			fileID := fmt.Sprintf("file-%04d", f)
			root, err = tree2.Insert(root, parentFn(targetDir, fileID), fileID,
				fmt.Sprintf("ref-v%d-%04d", i, f))
			if err != nil {
				b.Fatalf("Insert: %v", err)
			}
		}
		if err := ts2.FlushReachable(root); err != nil {
			b.Fatalf("FlushReachable: %v", err)
		}
	}
}
