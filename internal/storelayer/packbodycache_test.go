package storelayer

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/pkg/store/local"
)

// The bound is the point: whatever the size of the bodies, the cache holds at
// most its budget. A fixed entry count could not say that.
func TestPackBodyCache_StaysWithinItsByteBudget(t *testing.T) {
	const budget = 1000
	c, err := newPackBodyCache(budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		c.Add(fmt.Sprintf("packs/%d", i), bytes.Repeat([]byte("x"), 300))
		if got := c.byteLen(); got > budget {
			t.Fatalf("after %d adds the cache holds %d bytes, over the %d budget", i+1, got, budget)
		}
	}
	if c.len() == 0 {
		t.Error("everything was evicted")
	}
}

// Smaller bodies mean more of them cached — behaviour a fixed count cannot
// express, since it held four packs whether they were 512 KB or 8 MB.
func TestPackBodyCache_HoldsMoreOfSmallerBodies(t *testing.T) {
	fill := func(size int) int {
		c, err := newPackBodyCache(8000, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 100; i++ {
			c.Add(fmt.Sprintf("packs/%d", i), bytes.Repeat([]byte("x"), size))
		}
		return c.len()
	}
	if small, large := fill(200), fill(2000); small <= large {
		t.Errorf("cached %d small bodies and %d large; smaller should fit in greater number", small, large)
	}
}

// A body larger than the whole budget is still served, alone.
func TestPackBodyCache_KeepsABodyLargerThanTheBudget(t *testing.T) {
	c, err := newPackBodyCache(100, nil)
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("y"), 5000)
	c.Add("packs/big", big)
	if got, ok := c.Get("packs/big"); !ok || len(got) != len(big) {
		t.Fatalf("oversized body: got %d bytes, ok=%v", len(got), ok)
	}
}

func TestPackBodyCache_ReplaceAndRemoveAccountForBytes(t *testing.T) {
	c, err := newPackBodyCache(10000, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("z"), 400)
	for i := 0; i < 5; i++ {
		c.Add("packs/same", data)
	}
	if got := c.byteLen(); got != len(data) {
		t.Fatalf("holds %d bytes for one entry of %d", got, len(data))
	}
	c.Add("packs/other", data)
	c.Remove("packs/same")
	if got := c.byteLen(); got != len(data) {
		t.Fatalf("holds %d bytes after removing one of two %d-byte entries", got, len(data))
	}
}

// The pressure signal means "this promotion did not pay for itself", so it has
// to fire when the cache runs out of room and *only* then. A pack removed
// because it was deleted or its upload discarded is gone, not thrashing;
// marking it burned a slot in a bounded window that exists to track live packs,
// and a prune deleting many packs could flush out real penalties.
func TestPackBodyCache_PressureSignalIsNotRaisedByDeliberateRemoval(t *testing.T) {
	var evicted []string
	c, err := newPackBodyCache(1000, func(key string, _ int) { evicted = append(evicted, key) })
	if err != nil {
		t.Fatal(err)
	}

	c.Add("packs/deleted", bytes.Repeat([]byte("a"), 100))
	c.Remove("packs/deleted") // what discardPack and Repack do
	if len(evicted) != 0 {
		t.Errorf("deliberate Remove raised the pressure signal for %v", evicted)
	}
	if got := c.byteLen(); got != 0 {
		t.Errorf("byte accounting missed a deliberate removal: holds %d bytes", got)
	}

	c.Add("packs/first", bytes.Repeat([]byte("b"), 900))
	c.Add("packs/second", bytes.Repeat([]byte("c"), 900)) // over budget, evicts "first"
	if len(evicted) != 1 || evicted[0] != "packs/first" {
		t.Fatalf("running out of room did not raise the pressure signal: %v", evicted)
	}
}

// Replacing an entry is not pressure either, and must still keep the byte
// count honest.
func TestPackBodyCache_ReplacingAnEntryRaisesNoPressureSignal(t *testing.T) {
	var evicted []string
	c, err := newPackBodyCache(10000, func(key string, _ int) { evicted = append(evicted, key) })
	if err != nil {
		t.Fatal(err)
	}
	c.Add("packs/same", bytes.Repeat([]byte("a"), 400))
	c.Add("packs/same", bytes.Repeat([]byte("b"), 400))
	if len(evicted) != 0 {
		t.Errorf("replacing an entry raised the pressure signal for %v", evicted)
	}
	if got := c.byteLen(); got != 400 {
		t.Errorf("holds %d bytes for one 400-byte entry", got)
	}
}

// The regression this exists for (#458): reading across more packs than the old
// four-entry cache held made every pass re-read whole packfiles. With a budget
// that covers the working set, each pack is transferred about once.
func TestPackStore_DoesNotRereadPacksWhenTheWorkingSetFits(t *testing.T) {
	ctx := context.Background()

	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingRangeStore{ObjectStore: base}
	writer, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}

	// Eight packs, more than the four the count-based cache held.
	// More objects per pack than packPromoteAfter, so a pack is cached partway
	// through the first pass rather than being ranged-read for the whole test.
	const packs = 8
	var keys []string
	payload := bytes.Repeat([]byte("p"), 4*1024)
	for p := 0; p < packs; p++ {
		for i := 0; i < 3*packPromoteAfter; i++ {
			key := fmt.Sprintf("filemeta/%064x", p*100+i)
			if err := writer.Put(ctx, key, payload); err != nil {
				t.Fatal(err)
			}
			keys = append(keys, key)
		}
		if err := writer.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}

	reader, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	counting.fullGets, counting.rangeGets, counting.bytesRead = 0, 0, 0

	// Three passes over every object, which is what makes a too-small cache
	// visible: the second and third passes should be almost entirely hits.
	for pass := 0; pass < 3; pass++ {
		for _, key := range keys {
			if _, err := reader.Get(ctx, key); err != nil {
				t.Fatalf("pass %d, Get(%s): %v", pass, key, err)
			}
		}
	}

	// Assert on bytes, not on whole-pack count. A too-small cache does not
	// simply do more whole-pack transfers — ranged reads absorb most of the
	// extra misses (#452), so the count barely moves while the traffic does.
	stored, err := base.List(ctx, packPrefix)
	if err != nil {
		t.Fatal(err)
	}
	var packBytes int64
	for _, ref := range stored {
		n, err := base.Size(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		packBytes += n
	}

	// Three passes over a working set that fits should transfer each pack about
	// once. Allowing double leaves room for the misses before promotion caches
	// a pack, without leaving room for re-reading the set on every pass.
	if limit := 2 * packBytes; counting.bytesRead > limit {
		t.Errorf("read %d bytes across three passes over %d bytes of packs (limit %d); the working set is being re-read",
			counting.bytesRead, packBytes, limit)
	}
}

// The regression this exists for: a working set genuinely larger than
// whatever budget the cache is given, which will be true of *some*
// repository no matter how the budget is picked -- #460 fit six packs and
// #474 needed 37, and the next repository past that will need more still.
//
// Without onPackEvicted, a pack too big to keep re-earns none of its promotion
// cost, is evicted, and is promoted again on the next miss run -- forever, at
// a cost that grows with how long the access pattern continues rather than
// with the repository. With it, a pack that loses that bet once is marked and
// never promoted again, so the whole-pack cost is bounded by the number of
// packs touched, not by the number of passes over them -- the property that
// makes this correct at any repository size instead of only within whatever
// range was last measured.
func TestPackStore_ThrashingPackStopsBeingRepromoted(t *testing.T) {
	ctx := context.Background()

	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingRangeStore{ObjectStore: base}
	writer, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}

	const packs = 8
	const perPack = 3 * packPromoteAfter
	payload := bytes.Repeat([]byte("p"), 4*1024)
	keys := make([][]string, packs)
	for p := 0; p < packs; p++ {
		for i := 0; i < perPack; i++ {
			key := fmt.Sprintf("filemeta/%064x", p*1000+i)
			if err := writer.Put(ctx, key, payload); err != nil {
				t.Fatal(err)
			}
			keys[p] = append(keys[p], key)
		}
		if err := writer.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}

	stored, err := base.List(ctx, packPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != packs {
		t.Fatalf("wrote %d packs, want %d", len(stored), packs)
	}
	var largestPack int64
	for _, ref := range stored {
		n, err := base.Size(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if n > largestPack {
			largestPack = n
		}
	}

	// A budget that fits at most one pack at a time. The point is not to find
	// the "right" budget -- it is to see what happens once a repository has
	// outgrown whatever budget was chosen, which will eventually be true of
	// any fixed number.
	reader, err := NewPackStore(counting, withPackBodyCacheBudget(int(largestPack)))
	if err != nil {
		t.Fatal(err)
	}
	counting.fullGets, counting.rangeGets, counting.bytesRead = 0, 0, 0

	// Interleaved round-robin across packs, not one pack drained before the
	// next: check and restore visit a file's node, filemeta, content and
	// chunks in write order, which scatters across packs rather than draining
	// one at a time, and that scattering is what makes a promotion lose its
	// bet before it can pay for itself.
	const passes = 5
	for pass := 0; pass < passes; pass++ {
		for round := 0; round < perPack; round++ {
			for p := 0; p < packs; p++ {
				if _, err := reader.Get(ctx, keys[p][round]); err != nil {
					t.Fatalf("pass %d round %d pack %d: %v", pass, round, p, err)
				}
			}
		}
	}

	// Every pack may be promoted at most once, ever: it either earns the one
	// slot this budget holds, or it is marked and never promoted again.
	// Without that, this fails at roughly (passes x rounds / packPromoteAfter)
	// whole-pack transfers per pack -- dozens, not eight.
	if counting.fullGets > packs {
		t.Errorf("fullGets = %d over %d passes, want at most %d (one promotion per pack, ever); "+
			"a thrashing pack is still being repromoted instead of falling back to ranged reads",
			counting.fullGets, passes, packs)
	}
}

// A resident body keeps its hit tally however many other packs are read
// alongside it.
//
// This is the property #494 was filed for. The tally used to live in an LRU
// bounded by packMissWindow while the body lived in one bounded by bytes, so a
// repository of small packs could hold far more bodies than the tally window had
// room for. Reading past the window forgot the tally while the body was still
// resident, and the next pressure eviction then read zero hits and penalized a
// pack that had been serving fine — the exact failure that window's comment
// claimed to prevent.
//
// The reproduction needs all three conditions together, which is why it is
// spelled out rather than folded into an existing test: the bodies must fit
// (so nothing is evicted early), every filler must be *read* (so it occupies a
// tally slot), and the earner must be evicted last (so its tally is consulted
// after the window has overflowed).
func TestPackBodyCache_ResidentBodyKeepsItsHitsPastTheMissWindow(t *testing.T) {
	const fillers = packMissWindow * 3
	const bodySize = 16

	var penalized []string
	// Roomy enough that every small body stays resident; the final oversized
	// Add is what forces an eviction.
	c, err := newPackBodyCache((fillers+2)*bodySize, func(key string, hits int) {
		if hits < packPromoteAfter {
			penalized = append(penalized, key)
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	c.Add("packs/earner", bytes.Repeat([]byte("e"), bodySize))
	for range packPromoteAfter {
		if _, ok := c.Get("packs/earner"); !ok {
			t.Fatal("earner left the cache while it was being read")
		}
	}

	// Every filler is read once, so each takes a tally slot. Past
	// packMissWindow of them, a window-bounded tally has dropped the earner's.
	for i := range fillers {
		key := fmt.Sprintf("packs/filler-%03d", i)
		c.Add(key, bytes.Repeat([]byte("f"), bodySize))
		if _, ok := c.Get(key); !ok {
			t.Fatalf("%s left the cache while it was being read", key)
		}
	}
	// Deliberately not read again here: a Get would refresh its recency and put
	// it at the head, and the eviction below takes from the tail. The earner has
	// to be the oldest entry for its tally to be the one consulted.

	// Blow the budget so the tail is evicted and the tallies are consulted.
	c.Add("packs/flood", bytes.Repeat([]byte("x"), (fillers+2)*bodySize))

	for _, key := range penalized {
		if key == "packs/earner" {
			t.Fatalf("a pack that served %d hits was penalized after %d other packs were read",
				packPromoteAfter, fillers)
		}
	}
	if len(penalized) == 0 {
		t.Fatal("nothing was penalized, so the eviction path never ran and this test proves nothing")
	}
}
