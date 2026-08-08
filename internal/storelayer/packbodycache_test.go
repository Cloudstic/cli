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

// onEvict is what lets PackStore tell a promotion that earned back its cost
// from one that didn't; it has to fire for every way a body leaves the cache,
// not just the size-triggered path.
func TestPackBodyCache_OnEvictFiresForEveryRemoval(t *testing.T) {
	var evicted []string
	c, err := newPackBodyCache(1000, func(key string) { evicted = append(evicted, key) })
	if err != nil {
		t.Fatal(err)
	}

	c.Add("packs/explicit", bytes.Repeat([]byte("a"), 100))
	c.Remove("packs/explicit")
	if len(evicted) != 1 || evicted[0] != "packs/explicit" {
		t.Fatalf("explicit Remove did not fire onEvict: %v", evicted)
	}

	evicted = nil
	c.Add("packs/first", bytes.Repeat([]byte("b"), 900))
	c.Add("packs/second", bytes.Repeat([]byte("c"), 900)) // over budget, evicts "first"
	if len(evicted) != 1 || evicted[0] != "packs/first" {
		t.Fatalf("budget-triggered eviction did not fire onEvict: %v", evicted)
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
