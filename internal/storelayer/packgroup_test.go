package storelayer

import (
	"context"
	"fmt"
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

// A pack's budget belongs to the keys it was formed from. Reading something
// else that happens to live in the same pack is not one of them.
//
// Packs mix five namespaces, so this is the ordinary case rather than a corner:
// a traversal declares a batch of filemeta refs and then reads, undeclared, the
// content objects those refs name, the HAMT nodes it discovers mid-walk, and
// whatever path resolution needs — a good share of which sit in the very packs
// it just declared against. Counting each of those against the declared budget
// retires it early, and the declared reads behind them are handed to the
// estimator the declaration existed to replace.
func TestPackGroupPlan_UndeclaredReadKeepsDeclaredBudget(t *testing.T) {
	const ref = "packs/a"
	declared := []string{"filemeta/1", "filemeta/2", "filemeta/3", "filemeta/4"}

	plan := &packGroupPlan{
		declared: map[string]int{},
		count:    map[string]int{ref: len(declared)},
		bytes:    map[string]int64{ref: 4 * 1024},
		size:     map[string]int64{ref: 64 * 1024},
	}
	for _, key := range declared {
		plan.declared[key] = 1
	}

	// As many undeclared reads out of that pack as it has declared objects —
	// enough to spend the budget outright when they are counted against it.
	for i := range declared {
		plan.consume(fmt.Sprintf("content/%d", i), ref)
	}

	for _, key := range declared {
		if _, planned := plan.fetchWhole(key, ref); !planned {
			t.Fatalf("%s is no longer planned after %d undeclared reads of %s: "+
				"reads nobody declared spent the budget formed for these keys", key, len(declared), ref)
		}
	}

	// And the declared reads themselves still retire it, so a finished pass
	// cannot decide for a later one.
	for _, key := range declared {
		plan.consume(key, ref)
	}
	if _, planned := plan.fetchWhole(declared[0], ref); planned {
		t.Error("the pack is still planned after every declared object was read; a spent plan must retire")
	}
}

// The same property where it is paid for: at the backend, in requests.
//
// The declared reads happen last and against an evicted body, so what they cost
// is what the store decided about them rather than what an earlier phase left in
// cache. With the budget intact they cost the one transfer the plan states; with
// it spent by the undeclared reads in front of them they cost the probe sequence
// the plan exists to remove.
func TestPackStore_UndeclaredReadDoesNotSpendAPlannedPacksBudget(t *testing.T) {
	ctx := context.Background()
	const perPack = 100
	const declaredCount = 40

	keys, w := seedPacks(t, ctx, newLocal(t), 2, perPack)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	counter := newRangeCounter(newLocal(t))
	copyRepo(t, ctx, w, counter)

	// One pack's worth of residency, so a body does not survive the next pack's
	// arrival and the declared phase below has to be decided rather than served.
	reader, err := NewPackStore(counter, withPackBodyCacheBudget(1))
	if err != nil {
		t.Fatal(err)
	}

	packA, packB := keys[:perPack], keys[perPack:]
	declared, undeclared := packA[:declaredCount], packA[declaredCount:]

	plan := reader.PlanReads(ctx, declared)
	if len(plan.Groups) != 1 {
		t.Fatalf("declared keys formed %d groups, want 1: they share a pack", len(plan.Groups))
	}

	// Everything else in that pack, none of it declared.
	for _, key := range undeclared {
		if _, err := reader.Get(ctx, key); err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
	}

	// Push the body out, so the declared reads below are answered by the plan
	// rather than by whatever the reads above left resident.
	for _, key := range packB[:packPromoteAfter+1] {
		if _, err := reader.Get(ctx, key); err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
	}

	rangesBefore := counter.ranges
	_, fetchesBefore := counter.packFetches()

	for _, key := range declared {
		if _, err := reader.Get(ctx, key); err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
	}

	_, fetchesAfter := counter.packFetches()
	if ranged := counter.ranges - rangesBefore; ranged != 0 {
		t.Errorf("%d ranged reads serving %d declared keys, want 0: the plan stated this pack's worth, "+
			"and %d undeclared reads in front of them spent it", ranged, declaredCount, len(undeclared))
	}
	if fetched := fetchesAfter - fetchesBefore; fetched != 1 {
		t.Errorf("%d whole-pack transfers for one declared group, want 1", fetched)
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

// blockingRanger holds a ranged read open until released, so a test can do
// something to the store while a read is in flight.
type blockingRanger struct {
	store.ObjectStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingRanger) GetRange(ctx context.Context, key string, off, length int64) ([]byte, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return s.ObjectStore.(store.RangeGetter).GetRange(ctx, key, off, length)
}

func (s *blockingRanger) Unwrap() store.ObjectStore { return s.ObjectStore }

// A read decides admission against one plan, so it must spend its slot in that
// same plan.
//
// PlanReads replaces the recorded plan wholesale, and a read blocked on the
// backend can outlive the grouping it belongs to. Looking the plan up a second
// time after the transfer lets such a read consume from a plan formed after it
// started — which spends a declaration the new plan's own read then cannot
// find, and hands that read to the estimator. It is the defect this type was
// changed to fix, arriving through the store rather than through the plan.
func TestPackStore_ReadKeepsItsOwnPlanAcrossASwap(t *testing.T) {
	ctx := context.Background()
	keys, w := seedPacks(t, ctx, newLocal(t), 1, 20)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	blocker := &blockingRanger{
		ObjectStore: newLocal(t),
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	copyRepo(t, ctx, w, blocker.ObjectStore)

	reader, err := NewPackStore(blocker)
	if err != nil {
		t.Fatal(err)
	}

	// The first plan declares one key, so its read takes a ranged read and
	// blocks inside the backend.
	first := keys[0]
	reader.PlanReads(ctx, []string{first})

	done := make(chan error, 1)
	go func() {
		_, err := reader.Get(ctx, first)
		done <- err
	}()
	<-blocker.entered

	// A second grouping lands while that read is still in the backend, and it
	// declares the very same key.
	reader.PlanReads(ctx, []string{first})
	close(blocker.release)
	if err := <-done; err != nil {
		t.Fatalf("get %s: %v", first, err)
	}

	// The in-flight read belonged to the first plan. The second must still hold
	// the declaration it made.
	if _, planned := reader.groupPlan().fetchWhole(first, packRefOf(t, reader, first)); !planned {
		t.Error("the second plan lost its declaration to a read that started before it existed")
	}
}

func packRefOf(t *testing.T, s *PackStore, key string) string {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.catalog.Get(key)
	if !ok {
		t.Fatalf("key %s is not in the catalog", key)
	}
	return entry.PackRef
}
