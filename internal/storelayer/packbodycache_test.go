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
	c, err := newPackBodyCache(budget)
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
		c, err := newPackBodyCache(8000)
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
	c, err := newPackBodyCache(100)
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
	c, err := newPackBodyCache(10000)
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
