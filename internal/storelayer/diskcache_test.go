package storelayer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudstic/cli/pkg/store"
)

// onDiskSize is what a body of n bytes occupies as a cache entry: the fixed
// header, one hash per block, then the body.
func onDiskSize(n int) int64 {
	blocks := (n + cacheBlockBytes - 1) / cacheBlockBytes
	return int64(cacheHeaderFixed + blocks*sha256.Size + n)
}

func newDiskCache(t *testing.T, inner store.ObjectStore, budget int64) (*DiskCacheStore, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := NewDiskCacheStore(inner, dir, budget)
	if err != nil {
		t.Fatalf("NewDiskCacheStore: %v", err)
	}
	if c == nil {
		t.Fatal("NewDiskCacheStore returned nil for a usable directory")
	}
	return c, dir
}

// blobKey names a blob the way the engine does: the ref is *not* the hash of
// the bytes, which is the case the cache's own body hash exists for.
func blobKey(n int) string { return fmt.Sprintf("blob/%064x", n) }

func packKey(body []byte) string {
	sum := sha256.Sum256(body)
	return packPrefix + hex.EncodeToString(sum[:])
}

func mustPut(t *testing.T, ctx context.Context, s store.ObjectStore, key string, body []byte) {
	t.Helper()
	if err := s.Put(ctx, key, body); err != nil {
		t.Fatalf("Put(%s): %v", key, err)
	}
}

func TestDiskCacheStore_GetCachesAndServesLocally(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("blob body "), 512)
	key := blobKey(1)
	mustPut(t, ctx, base, key, body)

	c, dir := newDiskCache(t, base, DiskCacheBudget)
	for range 5 {
		got, err := c.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(got, body) {
			t.Fatal("body differs from what the backend holds")
		}
	}
	if n := base.gets[key]; n != 1 {
		t.Errorf("backend saw %d whole fetches for 5 reads, want 1", n)
	}
	if n := len(mustReadDir(t, dir)); n != 1 {
		t.Errorf("cache holds %d entries, want 1", n)
	}
}

// The rule the layer turns on: one ranged read is not evidence that an
// aggregate is being worked through, and fetching it whole on that evidence
// multiplies bytes transferred by the object-to-member ratio.
func TestDiskCacheStore_PromotesOnlyAfterARepeatedRangedRead(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("x"), 64<<10)
	key := blobKey(2)
	mustPut(t, ctx, base, key, body)

	c, _ := newDiskCache(t, base, DiskCacheBudget)

	// First ranged read: served remotely, nothing cached.
	if _, err := c.GetRange(ctx, key, 0, 16); err != nil {
		t.Fatal(err)
	}
	if base.ranges != 1 || base.gets[key] != 0 {
		t.Fatalf("after one ranged read: %d ranges, %d whole fetches; want 1 and 0", base.ranges, base.gets[key])
	}

	// Second: promotes to a whole transfer.
	if _, err := c.GetRange(ctx, key, 16, 16); err != nil {
		t.Fatal(err)
	}
	if base.gets[key] != 1 {
		t.Fatalf("second ranged read made %d whole fetches, want 1", base.gets[key])
	}

	// Everything after is local.
	beforeRanges, beforeGets := base.ranges, base.gets[key]
	for i := range 50 {
		got, err := c.GetRange(ctx, key, int64(i), 32)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, body[i:i+32]) {
			t.Fatalf("range at %d differs from the backend's bytes", i)
		}
	}
	if base.ranges != beforeRanges || base.gets[key] != beforeGets {
		t.Errorf("50 cached ranged reads cost %d ranges and %d fetches, want 0 and 0",
			base.ranges-beforeRanges, base.gets[key]-beforeGets)
	}
}

// A blob ref is the hash of its members' digests, not of its bytes, so the
// name proves nothing about what is on disk. The stored body hash is what
// makes a cached entry verifiable for both namespaces.
func TestDiskCacheStore_RejectsAndDropsCorruptedEntry(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("original "), 256)
	key := blobKey(3)
	mustPut(t, ctx, base, key, body)

	c, dir := newDiskCache(t, base, DiskCacheBudget)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, entryName(key))
	// The real entry with its body overwritten: same name, same length, same
	// header, different bytes. This is what a per-block hash must catch and
	// what a filename cannot, since a blob ref is not the hash of its bytes.
	corrupt := encodeEntry(body)
	bodyAt := len(corrupt) - len(body)
	copy(corrupt[bodyAt:], bytes.Repeat([]byte("wrong    "), 256))
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after corruption: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("a corrupted entry was served instead of the backend's bytes")
	}
	c.mu.Lock()
	used := c.used
	c.mu.Unlock()
	// Refetched and re-cached, so exactly one entry's worth.
	if want := onDiskSize(len(body)); used != want {
		t.Errorf("used = %d after refetch, want %d", used, want)
	}
	if base.gets[key] != 2 {
		t.Errorf("backend saw %d fetches, want 2 (initial plus the refetch)", base.gets[key])
	}
}

// The exclusion list is the whole rule, so it is the thing worth pinning: a
// mutable key that reached the cache would be served stale, which is the one
// failure the immutability argument does not cover.
func TestDiskCacheStore_NeverCachesMutableKeys(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	c, dir := newDiskCache(t, base, DiskCacheBudget)

	body := []byte("some bytes")
	for _, key := range []string{
		"index/latest",
		"index/snapshots",
		"index/packs",
		store.KeySlotPrefix + "password",
		"config",
	} {
		mustPut(t, ctx, base, key, body)
		for range 4 {
			if _, err := c.Get(ctx, key); err != nil {
				t.Fatalf("Get(%s): %v", key, err)
			}
		}
		if n := base.gets[key]; n != 4 {
			t.Errorf("%s: backend saw %d whole fetches for 4 reads, want 4 (uncached)", key, n)
		}
	}
	if n := len(mustReadDir(t, dir)); n != 0 {
		t.Fatalf("cache holds %d entries for mutable keys, want 0", n)
	}
}

// A mutable key whose value changes underneath must be re-read, not served
// from a copy. This is the failure the exclusion list prevents, asserted
// directly rather than through the list's contents.
func TestDiskCacheStore_MutableKeyReflectsAWrite(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	c, _ := newDiskCache(t, base, DiskCacheBudget)

	const key = "index/latest"
	mustPut(t, ctx, base, key, []byte("snapshot-a"))
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	mustPut(t, ctx, base, key, []byte("snapshot-b"))
	got, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "snapshot-b" {
		t.Fatalf("read back %q after an overwrite, want %q", got, "snapshot-b")
	}
}

// Every immutable namespace is cached, in both formats. The earlier version of
// this layer cached packs and blobs alone, which held all of a v2 repository
// and only the bodies of a v3 one.
func TestDiskCacheStore_CachesEveryImmutableNamespace(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	c, dir := newDiskCache(t, base, DiskCacheBudget)

	keys := []string{
		"node/" + strings.Repeat("a", 64),
		"chunk/" + strings.Repeat("c", 64),
		"snapshot/" + strings.Repeat("b", 64),
		"filemeta/" + strings.Repeat("d", 64),
		"content/" + strings.Repeat("e", 64),
		blobKey(42),
	}
	for _, key := range keys {
		mustPut(t, ctx, base, key, bytes.Repeat([]byte("i"), 512))
		for range 4 {
			if _, err := c.Get(ctx, key); err != nil {
				t.Fatalf("Get(%s): %v", key, err)
			}
		}
		if n := base.gets[key]; n != 1 {
			t.Errorf("%s: backend saw %d whole fetches for 4 reads, want 1", key, n)
		}
	}
	if n := len(mustReadDir(t, dir)); n != len(keys) {
		t.Fatalf("cache holds %d entries, want %d", n, len(keys))
	}
}

func TestDiskCacheStore_CachesBothPacksAndBlobs(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	c, dir := newDiskCache(t, base, DiskCacheBudget)

	packBody := bytes.Repeat([]byte("p"), 4096)
	pk := packKey(packBody)
	bk := blobKey(9)
	blobBody := bytes.Repeat([]byte("b"), 4096)
	mustPut(t, ctx, base, pk, packBody)
	mustPut(t, ctx, base, bk, blobBody)

	for _, k := range []string{pk, bk} {
		for range 3 {
			if _, err := c.Get(ctx, k); err != nil {
				t.Fatalf("Get(%s): %v", k, err)
			}
		}
		if n := base.gets[k]; n != 1 {
			t.Errorf("%s: %d whole fetches for 3 reads, want 1", k, n)
		}
	}
	if n := len(mustReadDir(t, dir)); n != 2 {
		t.Errorf("cache holds %d entries, want 2", n)
	}
}

// The budget is a bound on the bytes in the directory, and that is what every
// test of it measures. Asserting on the in-memory counter instead is what let
// three separate leaks through: the counter agreed with itself while the
// directory held nine times the budget.
//
// Eviction frees a slice of the budget rather than exactly the bytes in hand
// (see diskCacheEvictFraction), so the property is "within budget, newest
// kept, oldest gone first" and not an exact surviving count.
func TestDiskCacheStore_EvictsToStayWithinBudget(t *testing.T) {
	ctx := context.Background()
	const each = 1 << 10
	const room = 8
	budget := room * onDiskSize(each)
	base := newRangeCounter(newLocal(t))
	c, dir := newDiskCache(t, base, budget)

	keys := make([]string, room+4)
	for i := range keys {
		body := bytes.Repeat([]byte{byte('a' + i)}, each)
		keys[i] = blobKey(100 + i)
		mustPut(t, ctx, base, keys[i], body)
		if _, err := c.Get(ctx, keys[i]); err != nil {
			t.Fatal(err)
		}
		// Distinct modification times, so oldest-first is well defined rather
		// than resolved by whatever order the filesystem reports.
		stamp(t, dir, keys[i], int64(i))
		if on := dirBytes(t, dir); on > budget {
			t.Fatalf("after %d writes the directory holds %d bytes, over its %d budget", i+1, on, budget)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, entryName(keys[0]))); !os.IsNotExist(err) {
		t.Errorf("oldest entry survived eviction, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, entryName(keys[len(keys)-1]))); err != nil {
		t.Errorf("newest entry was not stored: %v", err)
	}
}

// An orphaned temp file is what a save killed between its write and its rename
// leaves behind, and nothing else removes it. Before they were swept, one 8 MB
// orphan under a 1 MiB budget left 9.0x the budget on disk and survived every
// eviction, because the constructor's accounting skipped `.tmp` names outright.
func TestDiskCacheStore_SweepsOrphanedTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	orphan := filepath.Join(dir, entryName(blobKey(1100))+".999"+tempSuffix)
	if err := os.WriteFile(orphan, bytes.Repeat([]byte("o"), 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	// Old enough that nothing can still be writing it.
	stale := time.Now().Add(-2 * tempStaleAfter)
	if err := os.Chtimes(orphan, stale, stale); err != nil {
		t.Fatal(err)
	}

	base := newRangeCounter(newLocal(t))
	const budget = 1 << 20
	c, err := NewDiskCacheStore(base, dir, budget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("a stale temp file survived the scan, stat err = %v", err)
	}

	for i := range 20 {
		key := blobKey(1100 + i)
		mustPut(t, ctx, base, key, bytes.Repeat([]byte{byte(i)}, 128<<10))
		if _, err := c.Get(ctx, key); err != nil {
			t.Fatal(err)
		}
		if on := dirBytes(t, dir); on > budget {
			t.Fatalf("after %d writes the directory holds %d bytes, over its %d budget", i+1, on, budget)
		}
	}
}

// The other half of the same rule: a temp file young enough to be a save in
// flight belongs to whoever is writing it. It is counted, because it occupies
// disk, and left alone, because deleting it would race that writer.
func TestDiskCacheStore_CountsButKeepsAnInFlightTempFile(t *testing.T) {
	dir := t.TempDir()
	inflight := filepath.Join(dir, entryName(blobKey(1200))+".1"+tempSuffix)
	const size = 4 << 10
	if err := os.WriteFile(inflight, bytes.Repeat([]byte("i"), size), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := NewDiskCacheStore(newRangeCounter(newLocal(t)), dir, DiskCacheBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inflight); err != nil {
		t.Errorf("a temp file that may still be being written was removed: %v", err)
	}
	c.mu.Lock()
	used := c.used
	c.mu.Unlock()
	if used != size {
		t.Errorf("used = %d, want %d: an in-flight temp file spends disk and must be counted", used, size)
	}
}

// Restarting must not reset the bound. Each run seeding an empty counter is
// how a long-lived cache directory grows without limit one process at a time.
func TestDiskCacheStore_StaysWithinBudgetAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base := newRangeCounter(newLocal(t))
	const each = 64 << 10
	budget := 4 * onDiskSize(each)

	for run := range 6 {
		c, err := NewDiskCacheStore(base, dir, budget)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 3 {
			key := blobKey(1300 + run*3 + i)
			mustPut(t, ctx, base, key, bytes.Repeat([]byte{byte(run)}, each))
			if _, err := c.Get(ctx, key); err != nil {
				t.Fatal(err)
			}
			if on := dirBytes(t, dir); on > budget {
				t.Fatalf("run %d write %d: directory holds %d bytes, over its %d budget", run, i, on, budget)
			}
		}
	}
}

// Two processes over one directory. Each used to enforce the budget against
// its own beliefs alone, so the directory reached a multiple of it — measured
// at 1.8x for two.
//
// The bound with several writers is the budget plus one headroom per other
// process, since that is how far behind the directory a process's estimate is
// allowed to drift before it looks again. It settles back to the budget as
// soon as any of them evicts.
func TestDiskCacheStore_StaysWithinBudgetWithASecondProcess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base := newRangeCounter(newLocal(t))
	const each = 64 << 10
	entry := onDiskSize(each)
	budget := 8 * entry

	first, err := NewDiskCacheStore(base, dir, budget)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDiskCacheStore(base, dir, budget)
	if err != nil {
		t.Fatal(err)
	}

	peak := budget + budget/diskCacheEvictFraction + entry
	for i := range 20 {
		for j, c := range []*DiskCacheStore{first, second} {
			key := blobKey(1400 + i*2 + j)
			mustPut(t, ctx, base, key, bytes.Repeat([]byte{byte(i)}, each))
			if _, err := c.Get(ctx, key); err != nil {
				t.Fatal(err)
			}
			if on := dirBytes(t, dir); on > peak {
				t.Fatalf("write %d by process %d: directory holds %d bytes, past the %d bound (budget %d)", i, j, on, peak, budget)
			}
		}
	}

	// And it converges: one more round through a single writer, which is
	// enough for it to look at the directory, brings it back inside.
	for i := range 4 {
		key := blobKey(1500 + i)
		mustPut(t, ctx, base, key, bytes.Repeat([]byte("z"), each))
		if _, err := first.Get(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if on := dirBytes(t, dir); on > budget {
		t.Errorf("directory settled at %d bytes, over its %d budget", on, budget)
	}
}

// A caller with no opinion about the budget gets the default, not a cache that
// silently stores nothing and not one with no bound at all.
func TestDiskCacheStore_NonPositiveBudgetFallsBackToTheDefault(t *testing.T) {
	for _, budget := range []int64{0, -1} {
		c, err := NewDiskCacheStore(newLocal(t), t.TempDir(), budget)
		if err != nil {
			t.Fatal(err)
		}
		if c.budget != DiskCacheBudget {
			t.Errorf("budget %d produced a cache bounded at %d, want the %d default", budget, c.budget, DiskCacheBudget)
		}
	}
}

// A body larger than the whole budget must be declined rather than triggering
// an eviction sweep that empties the cache and then fails to store it anyway.
func TestDiskCacheStore_DeclinesBodyLargerThanBudget(t *testing.T) {
	ctx := context.Background()
	const budget = 4 << 10
	base := newRangeCounter(newLocal(t))
	c, dir := newDiskCache(t, base, budget)

	small, big := blobKey(200), blobKey(201)
	mustPut(t, ctx, base, small, bytes.Repeat([]byte("s"), 128))
	mustPut(t, ctx, base, big, bytes.Repeat([]byte("B"), budget+1))

	if _, err := c.Get(ctx, small); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, big); err != nil {
		t.Fatal(err)
	}

	entries := mustReadDir(t, dir)
	if len(entries) != 1 {
		t.Fatalf("cache holds %d entries, want 1", len(entries))
	}
	if entries[0].Name() != entryName(small) {
		t.Error("an oversized body evicted the entry that fit")
	}
}

func TestDiskCacheStore_DeleteEvicts(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	c, dir := newDiskCache(t, base, DiskCacheBudget)

	key := blobKey(300)
	mustPut(t, ctx, base, key, bytes.Repeat([]byte("d"), 512))
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if n := len(mustReadDir(t, dir)); n != 0 {
		t.Errorf("cache holds %d entries after Delete, want 0", n)
	}
	c.mu.Lock()
	used := c.used
	c.mu.Unlock()
	if used != 0 {
		t.Errorf("used = %d after deleting the only entry, want 0", used)
	}
	if _, err := c.Get(ctx, key); err == nil {
		t.Error("a deleted object was served from cache")
	}
}

func TestDiskCacheStore_DeleteAllEvicts(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	c, dir := newDiskCache(t, base, DiskCacheBudget)

	keys := []string{blobKey(400), blobKey(401)}
	for _, k := range keys {
		mustPut(t, ctx, base, k, bytes.Repeat([]byte("e"), 256))
		if _, err := c.Get(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.DeleteAll(ctx, keys); err != nil {
		t.Fatal(err)
	}
	if n := len(mustReadDir(t, dir)); n != 0 {
		t.Errorf("cache holds %d entries after DeleteAll, want 0", n)
	}
}

// Reopening must adopt what a previous process left, including its size
// accounting — otherwise a long-lived cache directory grows without bound
// because each run believes it starts empty.
func TestDiskCacheStore_AdoptsExistingDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("persisted "), 64)
	key := blobKey(500)
	mustPut(t, ctx, base, key, body)

	first, err := NewDiskCacheStore(base, dir, DiskCacheBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Get(ctx, key); err != nil {
		t.Fatal(err)
	}

	second, err := NewDiskCacheStore(base, dir, DiskCacheBudget)
	if err != nil {
		t.Fatal(err)
	}
	before := base.gets[key]
	got, err := second.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("reopened cache served the wrong bytes")
	}
	if base.gets[key] != before {
		t.Error("reopened cache refetched an entry the previous one wrote")
	}
	second.mu.Lock()
	used := second.used
	second.mu.Unlock()
	if want := onDiskSize(len(body)); used != want {
		t.Errorf("reopened cache accounts %d bytes, want %d", used, want)
	}
}

func TestDiskCacheStore_EmptyDirDisablesCache(t *testing.T) {
	c, err := NewDiskCacheStore(newLocal(t), "", DiskCacheBudget)
	if err != nil {
		t.Fatalf("NewDiskCacheStore(\"\"): %v", err)
	}
	if c != nil {
		t.Fatal("an empty directory should disable the cache, not create one")
	}
}

func TestDiskCacheStore_UnusableDirIsReportedNotFatal(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewDiskCacheStore(newLocal(t), filepath.Join(blocker, "cache"), DiskCacheBudget)
	if err == nil {
		t.Error("an unusable directory returned no error")
	}
	if c != nil {
		t.Error("an unusable directory produced a live cache")
	}
}

// A range outside the object must fail the same way it does at the backend,
// rather than being clamped into a short read the AEAD above would report as
// a decryption failure.
func TestDiskCacheStore_RangeOutsideObjectErrors(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("z"), 1024)
	key := blobKey(600)
	mustPut(t, ctx, base, key, body)

	c, _ := newDiskCache(t, base, DiskCacheBudget)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]int64{{0, 2048}, {1000, 100}, {-1, 10}, {0, -1}} {
		if _, err := c.GetRange(ctx, key, r[0], r[1]); err == nil {
			t.Errorf("GetRange(%d, %d) on a %d-byte object returned no error", r[0], r[1], len(body))
		}
	}
}

func TestDiskCacheStore_ConcurrentReads(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	c, _ := newDiskCache(t, base, DiskCacheBudget)

	const objects = 8
	bodies := make([][]byte, objects)
	keys := make([]string, objects)
	for i := range keys {
		bodies[i] = bytes.Repeat([]byte{byte('A' + i)}, 4096)
		keys[i] = blobKey(700 + i)
		mustPut(t, ctx, base, keys[i], bodies[i])
	}

	var wg sync.WaitGroup
	for range 4 {
		for i := range keys {
			wg.Add(2)
			go func() {
				defer wg.Done()
				got, err := c.Get(ctx, keys[i])
				if err != nil {
					t.Errorf("Get(%s): %v", keys[i], err)
					return
				}
				if !bytes.Equal(got, bodies[i]) {
					t.Errorf("%s: wrong body", keys[i])
				}
			}()
			go func() {
				defer wg.Done()
				got, err := c.GetRange(ctx, keys[i], 8, 64)
				if err != nil {
					t.Errorf("GetRange(%s): %v", keys[i], err)
					return
				}
				if !bytes.Equal(got, bodies[i][8:72]) {
					t.Errorf("%s: wrong range", keys[i])
				}
			}()
		}
	}
	wg.Wait()
}

func TestDiskCacheStore_LeavesNoTempFiles(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	c, dir := newDiskCache(t, base, DiskCacheBudget)
	for i := range 8 {
		key := blobKey(800 + i)
		mustPut(t, ctx, base, key, bytes.Repeat([]byte("t"), 512))
		if _, err := c.Get(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range mustReadDir(t, dir) {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// stamp gives an entry a modification time derived from seq, so a lower seq is
// strictly older. Eviction is oldest-first, and a filesystem's own timestamps
// are too coarse to order writes made microseconds apart.
func stamp(t *testing.T, dir, key string, seq int64) {
	t.Helper()
	path := filepath.Join(dir, entryName(key))
	when := time.Unix(1_700_000_000+seq, 0)
	if err := os.Chtimes(path, when, when); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
}

// dirBytes is what the cache directory actually occupies, temp files included.
// It is the quantity the budget bounds, and so the one every test of the
// budget asserts on: the in-memory counter is exactly what was wrong.
func dirBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	for _, e := range mustReadDir(t, dir) {
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	return entries
}

// A ranged read must verify, not just a whole one. Serving an unverified range
// is how a corrupted cache entry reaches the AEAD above and surfaces as a
// decryption failure on a healthy repository.
func TestDiskCacheStore_RangedReadVerifiesTheBlocksItTouches(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("abcdefgh"), cacheBlockBytes/8*4) // four blocks
	key := blobKey(900)
	mustPut(t, ctx, base, key, body)

	c, dir := newDiskCache(t, base, DiskCacheBudget)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}

	// Corrupt one byte inside the first block, leaving the header intact.
	path := filepath.Join(dir, entryName(key))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bodyAt := len(raw) - len(body)
	raw[bodyAt+10] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	before := backendReads(base, key)
	got, err := c.GetRange(ctx, key, 0, 64)
	if err != nil {
		t.Fatalf("GetRange after corruption: %v", err)
	}
	if !bytes.Equal(got, body[:64]) {
		t.Fatal("a corrupted range was served instead of the backend's bytes")
	}
	if backendReads(base, key) == before {
		t.Error("the corrupted entry was served without going back to the store")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the corrupted entry survived the read, stat err = %v", err)
	}
}

// The design's trade-off, stated as behaviour: verification covers the blocks
// a read touches and no others. That is what makes a 4 KB read cost 64 KB of
// hashing instead of the whole object — and it means damage elsewhere in the
// entry is found when something reads it, not before.
func TestDiskCacheStore_VerificationIsScopedToTheBlocksRead(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	const blocks = 8
	body := bytes.Repeat([]byte("q"), cacheBlockBytes*blocks)
	key := blobKey(901)
	mustPut(t, ctx, base, key, body)

	c, dir := newDiskCache(t, base, DiskCacheBudget)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}

	// Damage the last block only.
	path := filepath.Join(dir, entryName(key))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bodyAt := len(raw) - len(body)
	raw[bodyAt+cacheBlockBytes*(blocks-1)+5] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	before := backendReads(base, key)
	if _, err := c.GetRange(ctx, key, 0, 128); err != nil {
		t.Fatalf("reading an intact block: %v", err)
	}
	if backendReads(base, key) != before {
		t.Error("an intact block went back to the store because another block was damaged")
	}

	// Reading into the damaged block finds it.
	if _, err := c.GetRange(ctx, key, cacheBlockBytes*(blocks-1), 128); err != nil {
		t.Fatalf("reading the damaged block: %v", err)
	}
	if backendReads(base, key) == before {
		t.Error("the damaged block was served without going back to the store")
	}
}

// backendReads is every read of key that reached the backend, whole or ranged.
// A dropped cache entry is refetched by whichever shape the caller asked for,
// so counting one alone misses the refetch it was meant to observe.
func backendReads(s *rangeCountingStore, key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets[key] + s.ranges
}

// A cache directory written by a different layout must read as a miss, not be
// misparsed into a body: every length in the header comes from a file this
// process did not write.
func TestDiskCacheStore_RejectsAForeignEntryLayout(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("v"), 4096)
	key := blobKey(902)
	mustPut(t, ctx, base, key, body)

	c, dir := newDiskCache(t, base, DiskCacheBudget)
	path := filepath.Join(dir, entryName(key))

	for name, content := range map[string][]byte{
		"empty":              {},
		"truncated header":   {cacheFormatV1, 0, 0},
		"wrong version":      append([]byte{99}, bytes.Repeat([]byte{0}, 64)...),
		"absurd block count": append([]byte{cacheFormatV1, 0, 1, 0, 0, 0xff, 0xff, 0xff, 0xff}, bytes.Repeat([]byte{0}, 64)...),
		"zero block size":    append([]byte{cacheFormatV1, 0, 0, 0, 0, 0, 0, 0, 1}, bytes.Repeat([]byte{0}, 64)...),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := c.Get(ctx, key)
		if err != nil {
			t.Errorf("%s: Get returned %v, want a refetch", name, err)
			continue
		}
		if !bytes.Equal(got, body) {
			t.Errorf("%s: served %d bytes, want the backend's %d", name, len(got), len(body))
		}
	}
}

// Bypass is what makes `check` verify the repository rather than a local copy
// of it. It must stop reads being *served* from the cache and stop them
// *populating* it, since a full-repository sweep would otherwise evict the
// entries the cache exists to hold.
func TestDiskCacheStore_BypassReadsPastTheCacheAndDoesNotPopulateIt(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("verified "), 512)
	key := blobKey(1000)
	mustPut(t, ctx, base, key, body)

	c, dir := newDiskCache(t, base, DiskCacheBudget)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	before := backendReads(base, key)

	restore := c.BypassReads()

	got, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("bypassed read returned the wrong bytes")
	}
	if backendReads(base, key) == before {
		t.Error("a bypassed read was served from the cache")
	}
	if _, err := c.GetRange(ctx, key, 0, 32); err != nil {
		t.Fatal(err)
	}

	// A key never seen before must not land in the cache while bypassed.
	fresh := blobKey(1001)
	mustPut(t, ctx, base, fresh, bytes.Repeat([]byte("n"), 256))
	if _, err := c.Get(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range mustReadDir(t, dir) {
		names[e.Name()] = true
	}
	if names[entryName(fresh)] {
		t.Error("a bypassed read populated the cache")
	}
	if !names[entryName(key)] {
		t.Error("bypass evicted an entry that was already cached")
	}

	// Releasing brings the cache back.
	restore()
	before = backendReads(base, key)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	if backendReads(base, key) != before {
		t.Error("the cache did not resume serving after bypass was cleared")
	}
}

func TestDiskCacheStore_NilBypassIsSafe(t *testing.T) {
	var c *DiskCacheStore
	c.BypassReads()()
}

// Bypass nests. A boolean restored to its previous value does not: two checks
// overlapping on one client would have the first to finish turn the cache back
// on underneath the second, which would then verify a local copy of the
// repository — the one thing the bypass exists to prevent.
func TestDiskCacheStore_BypassNestsAcrossOverlappingCallers(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("nested "), 128)
	key := blobKey(1600)
	mustPut(t, ctx, base, key, body)

	c, _ := newDiskCache(t, base, DiskCacheBudget)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}

	outer := c.BypassReads()
	inner := c.BypassReads()
	inner() // the second caller finishes first

	before := backendReads(base, key)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	if backendReads(base, key) == before {
		t.Error("the cache came back while a caller still held the bypass")
	}

	outer()
	before = backendReads(base, key)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	if backendReads(base, key) != before {
		t.Error("the cache did not resume once every caller had released")
	}

	// Releasing twice must not credit the count twice, or an unbalanced caller
	// would leave the cache bypassed for the life of the client.
	outer()
	before = backendReads(base, key)
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	if backendReads(base, key) != before {
		t.Error("a repeated release disabled the cache")
	}
}

// A range whose offset and length sum past MaxInt64 must be rejected, not
// wrapped into a negative that passes the bounds test and then sizes an
// allocation. Both paths are covered: the cached one slices a verified entry,
// the promoted one slices a body just fetched.
func TestDiskCacheStore_RejectsAnOverflowingRange(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("w"), 4096)
	key := blobKey(1601)
	mustPut(t, ctx, base, key, body)

	c, _ := newDiskCache(t, base, DiskCacheBudget)

	// Uncached: the first ranged read is served remotely, the second promotes
	// and slices the fetched body.
	for range 2 {
		if _, err := c.GetRange(ctx, key, math.MaxInt64, 1); err == nil {
			t.Error("an overflowing range on an uncached object returned no error")
		}
	}
	if _, err := c.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]int64{{math.MaxInt64, 1}, {1, math.MaxInt64}, {math.MaxInt64, math.MaxInt64}} {
		if _, err := c.GetRange(ctx, key, r[0], r[1]); err == nil {
			t.Errorf("GetRange(%d, %d) on a cached %d-byte object returned no error", r[0], r[1], len(body))
		}
	}
}

// blockingStore lets a test hold a Get open, so a Delete can be landed in the
// window between the backend read and the save that follows it.
type blockingStore struct {
	store.ObjectStore
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *blockingStore) Get(ctx context.Context, key string) ([]byte, error) {
	body, err := s.ObjectStore.Get(ctx, key)
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return body, err
}

// A read that began before a deletion must not write its result afterwards.
//
// Without the guard the sequence is: the read misses and fetches from the
// store; Delete drops the entry and removes the object; the read completes and
// saves. The cache then holds an object the repository does not, and serves it.
func TestDiskCacheStore_ReadInFlightDuringADeleteDoesNotRepopulate(t *testing.T) {
	ctx := context.Background()
	inner := newLocal(t)
	blocking := &blockingStore{
		ObjectStore: inner,
		release:     make(chan struct{}),
		entered:     make(chan struct{}),
	}
	c, dir := newDiskCache(t, blocking, DiskCacheBudget)

	key := blobKey(1100)
	mustPut(t, ctx, inner, key, bytes.Repeat([]byte("doomed "), 256))

	done := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, key)
		done <- err
	}()

	<-blocking.entered // the backend read has returned, the save has not run
	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	close(blocking.release)
	if err := <-done; err != nil {
		t.Fatalf("Get: %v", err)
	}

	if n := len(mustReadDir(t, dir)); n != 0 {
		t.Errorf("cache holds %d entries for a deleted object; a read that began "+
			"before the delete wrote its result afterwards", n)
	}
	if _, err := c.Get(ctx, key); err == nil {
		t.Error("a deleted object was served from the cache")
	}
}
