package storelayer

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// rangeCountingStore counts what a read actually costs at the backend: whole
// object transfers and ranged reads, separately, because the whole point of
// admission is choosing between them.
type rangeCountingStore struct {
	store.ObjectStore
	mu     sync.Mutex
	gets   map[string]int
	ranges int
}

func newRangeCounter(inner store.ObjectStore) *rangeCountingStore {
	return &rangeCountingStore{ObjectStore: inner, gets: map[string]int{}}
}

func (s *rangeCountingStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	s.gets[key]++
	s.mu.Unlock()
	return s.ObjectStore.Get(ctx, key)
}

func (s *rangeCountingStore) GetRange(ctx context.Context, key string, off, length int64) ([]byte, error) {
	s.mu.Lock()
	s.ranges++
	s.mu.Unlock()
	return s.ObjectStore.(store.RangeGetter).GetRange(ctx, key, off, length)
}

func (s *rangeCountingStore) Unwrap() store.ObjectStore { return s.ObjectStore }

func (s *rangeCountingStore) packFetches() (packs, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, n := range s.gets {
		if strings.HasPrefix(key, packPrefix) {
			packs++
			total += n
		}
	}
	return packs, total
}

// The property the whole change exists for: a caller that reads in the groups
// the store handed it transfers each pack exactly once, with no probe sequence
// in front of it.
//
// Without grouping, PackStore learns a pack is worth transferring by
// ranged-reading packPromoteAfter-1 objects out of it first, so the same
// workload costs those probes on every pack. The plan states the number the
// probes exist to estimate, so they do not happen at all.
func TestPackStore_GroupedReadsTransferEachPackOnce(t *testing.T) {
	ctx := context.Background()
	const packs, perPack = 4, 40

	keys, w := seedPacks(t, ctx, newLocal(t), packs, perPack)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	counter := newRangeCounter(newLocal(t))
	copyRepo(t, ctx, w, counter)

	reader, err := NewPackStore(counter)
	if err != nil {
		t.Fatal(err)
	}
	// Scatter the keys so that reading them in the given order would interleave
	// packs; grouping is what undoes that.
	scattered := interleave(keys, packs, perPack)

	for _, group := range reader.PlanReads(ctx, scattered).Groups {
		for _, key := range group {
			if _, err := reader.Get(ctx, key); err != nil {
				t.Fatalf("get %s: %v", key, err)
			}
		}
	}

	fetched, total := counter.packFetches()
	if fetched != packs {
		t.Errorf("touched %d packs, want %d", fetched, packs)
	}
	if total != packs {
		t.Errorf("%d whole-pack transfers for %d packs; a pack was transferred more than once", total, packs)
	}
	if counter.ranges != 0 {
		t.Errorf("%d ranged reads; grouped reads should need no probe sequence", counter.ranges)
	}
}

// A read the plan says nothing about must still work, and must still be bounded:
// one object wanted from a pack is a ranged read, not an 8 MB transfer.
func TestPackStore_UngroupedReadStillProbes(t *testing.T) {
	ctx := context.Background()
	keys, w := seedPacks(t, ctx, newLocal(t), 1, 20)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	counter := newRangeCounter(newLocal(t))
	copyRepo(t, ctx, w, counter)
	reader, err := NewPackStore(counter)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := reader.Get(ctx, keys[0]); err != nil {
		t.Fatal(err)
	}
	if counter.ranges != 1 {
		t.Errorf("ranged reads = %d, want 1: an unplanned single read must not transfer the pack", counter.ranges)
	}
	if _, total := counter.packFetches(); total != 0 {
		t.Errorf("%d whole-pack transfers for a single unplanned read", total)
	}
}

// copyRepo copies every object of a flushed PackStore's backend into dst.
func copyRepo(t *testing.T, ctx context.Context, src *PackStore, dst store.ObjectStore) {
	t.Helper()
	inner := src.ObjectStore
	objs, err := inner.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range objs {
		data, err := inner.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := dst.Put(ctx, key, data); err != nil {
			t.Fatal(err)
		}
	}
}

// interleave returns the keys ordered so consecutive entries come from
// different packs, which is what an unsorted walk over an aged repository
// produces.
func interleave(keys []string, packs, perPack int) []string {
	out := make([]string, 0, len(keys))
	for i := 0; i < perPack; i++ {
		for p := 0; p < packs; p++ {
			out = append(out, keys[p*perPack+i])
		}
	}
	return out
}

func newLocal(t *testing.T) store.ObjectStore {
	t.Helper()
	s, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
