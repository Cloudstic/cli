package storelayer

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/pkg/store/local"
)

// A declaration states *what* will be read. Admission needs to know *when*, and
// these two tests are the difference between the two, isolated.
//
// PlanReads records how many objects a caller wants from each pack and how many
// bytes they come to, and resolveFromPack promotes a pack to a whole transfer
// once those quantities say a transfer is cheaper than the ranged reads it
// replaces. That arithmetic is right only if the caller reads a pack's declared
// objects while the pack body is still resident. Nothing in a declaration says
// so. Restore's metadata phase earns the arithmetic — it reads group by group,
// with concurrency bounded to the body cache's capacity. Its write phase
// briefly declared the same way (#496) while writing in walk order across every
// pack at once, and that is what RFC 0025 §7 removed.
//
// The declared set is identical in both. Only the consumption order differs,
// and it is what decides whether declaring helps or produces RFC 0025 §5's
// refetch storm.

// seedOversizedWorkingSet builds a repository whose packs cannot all be
// resident at once, and returns a reader over it, a request counter, and its
// keys grouped by pack.
//
// residentPacks is how many pack bodies the reader's cache may hold, and the
// budget is derived from the packs that were actually written rather than
// written down. That is the whole difficulty of provoking this: the body cache
// bounds *bytes*, not pack count, so "more packs than the cache holds" is not a
// property of the pack count at all. A repository of small packs keeps every
// body resident however many there are — which is why an aging benchmark can
// reach 22 packs and show nothing (RFC 0025 §7), and why a budget of
// 2*maxPackSize against 64 KB packs measures the in-cache case twice.
func seedOversizedWorkingSet(t *testing.T, ctx context.Context, residentPacks int) (*PackStore, *countingRangeStore, [][]string) {
	t.Helper()

	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const packs, perPack = 12, 16
	keys, writer := seedPacks(t, ctx, base, packs, perPack)
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	byPack := make([][]string, packs)
	for p := range packs {
		byPack[p] = keys[p*perPack : (p+1)*perPack]
	}

	entry, ok := writer.catalog.Get(keys[0])
	if !ok {
		t.Fatalf("key %s is not in the catalog", keys[0])
	}
	packBytes, ok := writer.catalog.PackExtent(entry.PackRef)
	if !ok || packBytes <= 0 {
		t.Fatalf("pack %s has no extent", entry.PackRef)
	}
	budget := int(packBytes) * residentPacks
	if budget >= int(packBytes)*packs {
		t.Fatalf("budget %d holds all %d packs; nothing would evict", budget, packs)
	}

	// A fresh PackStore over the same backend, so reads start with an empty
	// body cache and an empty admission history rather than inheriting the
	// writer's.
	counting := &countingRangeStore{ObjectStore: base}
	reader, err := NewPackStore(counting, withPackBodyCacheBudget(budget))
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.ensureCatalogLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	return reader, counting, byPack
}

func readAll(t *testing.T, ctx context.Context, s *PackStore, keys []string) {
	t.Helper()
	for _, k := range keys {
		if _, err := s.Get(ctx, k); err != nil {
			t.Fatalf("get %s: %v", k, err)
		}
	}
}

// Consumed in the order PlanReads returned, a declaration is sound: a pack is
// wanted across one contiguous run and never again, so one transfer serves the
// whole run whatever the total size of the read set.
//
// This is restore's metadata phase, and it is the case declaring was built for.
func TestPackStore_DeclaredSetConsumedInPlanOrderTransfersEachPackOnce(t *testing.T) {
	ctx := context.Background()

	// Two packs resident against twelve packs of wanted objects: the working
	// set is six times what can be held, which is the condition that breaks the
	// out-of-order case below and must not break this one.
	reader, counting, byPack := seedOversizedWorkingSet(t, ctx, 2)

	var all []string
	for _, group := range byPack {
		all = append(all, group...)
	}

	plan := reader.PlanReads(ctx, all)
	for _, group := range plan.Groups {
		readAll(t, ctx, reader, group)
	}

	// One transfer per pack is the whole claim. Anything above it means a pack
	// was fetched, evicted and fetched again.
	if counting.fullGets > len(byPack) {
		t.Errorf("in-order consumption transferred %d pack bodies for %d packs; "+
			"a pack was refetched", counting.fullGets, len(byPack))
	}
}

// Consumed out of plan order, the same declaration is not just useless but
// actively harmful: it tells admission every pack is worth a whole transfer,
// and the reader then touches every pack before returning to any of them, so
// each body is evicted before its objects are consumed and fetched again.
//
// The declared set is identical to the test above. Only the order changed, and
// it costs 16x the bytes — which end to end was 18,416 MB against 160 MB on an
// 11-pack repository (RFC 0025 §7).
func TestPackStore_DeclaredSetConsumedOutOfPlanOrderRefetchesPacks(t *testing.T) {
	ctx := context.Background()

	reader, counting, byPack := seedOversizedWorkingSet(t, ctx, 2)

	var all []string
	for _, group := range byPack {
		all = append(all, group...)
	}
	reader.PlanReads(ctx, all)

	// Round-robin across packs — the plan's grouping discarded, which
	// store.PlanReads documents as a supported use and which the removed
	// write-phase declaration relied on. Every pack is touched before any pack
	// is revisited.
	perPack := len(byPack[0])
	var scattered []string
	for i := range perPack {
		for p := range byPack {
			scattered = append(scattered, byPack[p][i])
		}
	}
	readAll(t, ctx, reader, scattered)

	t.Logf("out-of-order: %d whole-pack transfers, %d ranged reads, %d bytes",
		counting.fullGets, counting.rangeGets, counting.bytesRead)

	// This asserts the *defect*, on purpose, and is a tripwire rather than a
	// guard: every read refetches a whole pack, because the planned path in
	// resolveFromPack consults neither the body cache's capacity nor the
	// eviction penalty that stops the estimator doing this. Nothing in the
	// shipped code declares a set it will not consume in order — restore's
	// write phase used to (#496) and stopped (RFC 0025 §7) — so no operation
	// reaches this today.
	//
	// If this test ever fails, admission has been made residency-aware and the
	// deletion it justifies is worth revisiting. Update it deliberately; do not
	// relax it.
	if counting.fullGets < len(scattered) {
		t.Errorf("out-of-order consumption transferred %d pack bodies for %d reads, "+
			"fewer than one per read: admission appears to bound residency now, so "+
			"RFC 0025 §7's reason for declaring nothing in restore's write phase "+
			"no longer holds", counting.fullGets, len(scattered))
	}
}
