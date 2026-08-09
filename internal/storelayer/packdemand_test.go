package storelayer

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// newDemandFixture writes objects into one pack and returns a *fresh* reader
// over a counting backend, so nothing is served from a warm cache and every
// read shows up in the counters.
//
// objects is deliberately a parameter: the whole behaviour under test turns on
// how it compares with packPromoteAfter.
func newDemandFixture(t *testing.T, objects int) (context.Context, *PackStore, *countingRangeStore, []string) {
	t.Helper()
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
	payload := bytes.Repeat([]byte("d"), 4*1024)
	keys := make([]string, 0, objects)
	for i := range objects {
		key := fmt.Sprintf("filemeta/%064x", i)
		if err := writer.Put(ctx, key, payload); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	// Load the catalog directly rather than through a Get. A warm-up Get would
	// also count as a miss against the pack, leaving the miss counter at 1
	// before the test starts and quietly shortening the probe sequence it is
	// measuring.
	if err := reader.ensureCatalogLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	counting.fullGets, counting.rangeGets, counting.bytesRead = 0, 0, 0
	counting.rangeCalls = nil

	return ctx, reader, counting, keys
}

func packRefOf(t *testing.T, s *PackStore, key string) string {
	t.Helper()
	entry, ok := s.catalog.Get(key)
	if !ok {
		t.Fatalf("key %s is not in the catalog", key)
	}
	return entry.PackRef
}

func readAll(t *testing.T, ctx context.Context, s *PackStore, keys []string) {
	t.Helper()
	for _, key := range keys {
		if _, err := s.Get(ctx, key); err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
	}
}

// The cost this exists to remove. Undeclared, a pack worth transferring whole
// is discovered to be so only after packPromoteAfter-1 ranged reads; declared,
// the first contact transfers it.
func TestPackStore_DeclaredDemandSkipsTheProbeSequence(t *testing.T) {
	const objects = packPromoteAfter * 2

	ctx, undeclared, undeclaredCounts, keys := newDemandFixture(t, objects)
	readAll(t, ctx, undeclared, keys)

	if undeclaredCounts.rangeGets != packPromoteAfter-1 {
		t.Fatalf("baseline probed %d times, expected %d — the premise of this test has moved",
			undeclaredCounts.rangeGets, packPromoteAfter-1)
	}

	ctx, declared, declaredCounts, keys := newDemandFixture(t, objects)
	release := declared.DeclareDemand(keys, store.DemandFinal)
	defer release()
	readAll(t, ctx, declared, keys)

	if declaredCounts.rangeGets != 0 {
		t.Errorf("declared read still issued %d ranged probes, want 0", declaredCounts.rangeGets)
	}
	if declaredCounts.fullGets != 1 {
		t.Errorf("declared read pulled the pack %d times, want exactly 1", declaredCounts.fullGets)
	}
}

// The other half of the decision: a pack a caller barely touches must not be
// transferred whole just because the count is known. Knowing the number is not
// a reason to fetch more than it justifies.
func TestPackStore_SmallDeclaredDemandStaysRanged(t *testing.T) {
	const objects = packPromoteAfter * 2
	ctx, s, counts, keys := newDemandFixture(t, objects)

	few := keys[:packPromoteAfter-1]
	release := s.DeclareDemand(few, store.DemandFinal)
	defer release()
	readAll(t, ctx, s, few)

	if counts.fullGets != 0 {
		t.Errorf("pulled %d whole packs for %d declared objects, want 0", counts.fullGets, len(few))
	}
	if counts.rangeGets != len(few) {
		t.Errorf("issued %d ranged reads for %d declared objects, want one each", counts.rangeGets, len(few))
	}
}

// Exhausting a PARTIAL declaration must not drop the pack body. A restore's
// first pass names the metadata objects it will read and cannot name the
// content objects those point at until it has read them, so its packs run out
// of declared demand long before the operation is done with them.
//
// Pinned as a regression test with its cost: dropping bodies on this signal was
// implemented and measured, and took restore at 42 packs from 507 requests /
// 320.8 MB to 937 / 647.0 MB — bytes almost exactly doubling.
func TestPackStore_ExhaustedPartialDemandKeepsThePackCached(t *testing.T) {
	const objects = packPromoteAfter * 2
	ctx, s, counts, keys := newDemandFixture(t, objects)

	release := s.DeclareDemand(keys, store.DemandPartial)
	defer release()
	readAll(t, ctx, s, keys)

	if s.packCache.byteLen() == 0 {
		t.Fatal("pack body was dropped when partial demand hit zero; a later pass must not have to re-fetch it")
	}

	// The read that stands in for a later pass touching the same pack: it must
	// be served from the cached body, not a second transfer.
	before := counts.fullGets
	if _, err := s.Get(ctx, keys[0]); err != nil {
		t.Fatal(err)
	}
	if counts.fullGets != before {
		t.Errorf("re-reading after partial demand was exhausted pulled the pack again (%d -> %d)", before, counts.fullGets)
	}
}

// Exhausting a FINAL declaration does drop the body, which is the half of demand
// counting that bounds residency: the cache then holds only packs something
// still wants, rather than whatever was touched most recently — a signal that is
// actively misleading for a traversal reading each object once.
func TestPackStore_ExhaustedFinalDemandReleasesThePack(t *testing.T) {
	const objects = packPromoteAfter * 2
	ctx, s, _, keys := newDemandFixture(t, objects)

	release := s.DeclareDemand(keys, store.DemandFinal)
	defer release()

	readAll(t, ctx, s, keys[:len(keys)-1])
	if s.packCache.byteLen() == 0 {
		t.Fatal("pack body was dropped before its declared demand was exhausted")
	}

	readAll(t, ctx, s, keys[len(keys)-1:])
	if got := s.packCache.byteLen(); got != 0 {
		t.Errorf("pack body still holds %d bytes after every declared object was read", got)
	}
}

// The restore sequence, which is the case the scope distinction exists for: a
// partial declaration is made and released, a final one covering the same pack
// follows, and only the second may release the body.
//
// Written as one test rather than trusting the two above to compose, because
// the ordering is the part that is easy to get wrong — the partial declaration
// is released *before* the final one is made, so nothing but the recorded scope
// distinguishes the two exhaustion events.
func TestPackStore_PartialThenFinalReleasesOnlyAtTheEnd(t *testing.T) {
	const objects = packPromoteAfter * 2
	ctx, s, counts, keys := newDemandFixture(t, objects)

	firstPass, secondPass := keys[:objects/2], keys[objects/2:]

	releasePartial := s.DeclareDemand(firstPass, store.DemandPartial)
	readAll(t, ctx, s, firstPass)
	releasePartial()

	if s.packCache.byteLen() == 0 {
		t.Fatal("pack released after the partial pass; the final pass would have to re-fetch it")
	}
	fetchesAfterFirstPass := counts.fullGets

	releaseFinal := s.DeclareDemand(secondPass, store.DemandFinal)
	defer releaseFinal()
	readAll(t, ctx, s, secondPass)

	if counts.fullGets != fetchesAfterFirstPass {
		t.Errorf("the final pass re-fetched the pack (%d -> %d); it should have been cached throughout",
			fetchesAfterFirstPass, counts.fullGets)
	}
	if got := s.packCache.byteLen(); got != 0 {
		t.Errorf("pack still holds %d bytes after the final declaration was exhausted", got)
	}
}

// A released pack must not look like a promotion that failed to pay for itself.
// packBodyCache distinguishes deliberate removal from pressure eviction for
// exactly this reason, and reading demand exhaustion as a penalty would put the
// pack back on ranged reads the next time it is wanted.
func TestPackStore_ReleasingOnZeroDemandDoesNotPenalizeThePack(t *testing.T) {
	const objects = packPromoteAfter * 2
	ctx, s, _, keys := newDemandFixture(t, objects)

	release := s.DeclareDemand(keys, store.DemandFinal)
	readAll(t, ctx, s, keys)
	release()

	packRef := packRefOf(t, s, keys[0])
	if _, penalized := s.admission.penalized.Peek(packRef); penalized {
		t.Error("pack was penalized for finishing its declared demand")
	}
}

// Everything that did not declare keeps the heuristics, which is what leaves
// cat, dedup probes during backup, and any kind an operation did not count on
// the behaviour their comments justify.
func TestPackStore_UndeclaredReadsKeepTheHeuristic(t *testing.T) {
	const objects = packPromoteAfter * 2
	ctx, s, counts, keys := newDemandFixture(t, objects)

	// Declare one pack's worth, then read a different set entirely. The
	// declaration must not change how the undeclared reads are served.
	release := s.DeclareDemand(nil, store.DemandFinal)
	defer release()
	readAll(t, ctx, s, keys)

	if counts.rangeGets != packPromoteAfter-1 {
		t.Errorf("undeclared read issued %d ranged probes, want the usual %d",
			counts.rangeGets, packPromoteAfter-1)
	}
}

// Release is deferred by callers, and a caller that also releases explicitly on
// an early return would otherwise subtract twice — retiring demand that a
// concurrent declaration still holds.
func TestPackStore_ReleaseIsIdempotent(t *testing.T) {
	const objects = packPromoteAfter * 2
	ctx, s, _, keys := newDemandFixture(t, objects)
	_ = ctx

	first := s.DeclareDemand(keys, store.DemandFinal)
	second := s.DeclareDemand(keys, store.DemandFinal)

	packRef := packRefOf(t, s, keys[0])
	if got := s.demand.outstanding(packRef); got != 2*objects {
		t.Fatalf("two declarations of %d objects gave outstanding=%d, want %d", objects, got, 2*objects)
	}

	first()
	first()
	first()

	if got := s.demand.outstanding(packRef); got != objects {
		t.Errorf("outstanding=%d after releasing one declaration three times, want %d", got, objects)
	}
	second()
	if got := s.demand.outstanding(packRef); got != 0 {
		t.Errorf("outstanding=%d after releasing both declarations, want 0", got)
	}
}

// Declaring must never make a read fail or force I/O. It is a hint, and a hint
// that can error is a liability: it would fail operations that would otherwise
// have succeeded.
func TestPackStore_DeclareDemandToleratesUnknownKeysAndLoadsNothing(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingRangeStore{ObjectStore: base}
	s, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}

	release := s.DeclareDemand([]string{"filemeta/nope", "chunk/also-nope"}, store.DemandFinal)
	defer release()

	if counting.fullGets != 0 || counting.rangeGets != 0 {
		t.Errorf("declaring performed I/O: %d full, %d ranged", counting.fullGets, counting.rangeGets)
	}
	if s.catalogLoaded {
		t.Error("declaring forced a catalog load; it must stay a no-I/O hint")
	}
	_ = ctx
}

// A read of a namespace nobody declared must not retire demand belonging to
// another. A pack mixes namespaces, and an operation reads them in different
// passes: restore reads content/ objects and then the chunk/ objects those name.
// Counting them together let a chunk read drain the pack's outstanding content
// demand and release the body while later files still wanted it.
//
// Measured before it was understood — release keyed on one count per pack cost
// 481 requests and 311.7 MB against 460 and 305.2 MB for not releasing at all.
func TestPackStore_UndeclaredKindDoesNotRetireDeclaredDemand(t *testing.T) {
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
	payload := bytes.Repeat([]byte("d"), 4*1024)
	// One pack holding both namespaces, which is the ordinary case: packing is
	// by size, not by kind.
	var contentKeys, chunkKeys []string
	for i := range packPromoteAfter * 2 {
		ck := fmt.Sprintf("content/%064x", i)
		hk := fmt.Sprintf("chunk/%064x", i)
		if err := writer.Put(ctx, ck, payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Put(ctx, hk, payload); err != nil {
			t.Fatal(err)
		}
		contentKeys = append(contentKeys, ck)
		chunkKeys = append(chunkKeys, hk)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.ensureCatalogLoaded(ctx); err != nil {
		t.Fatal(err)
	}

	// Declare only the content objects, as restore's write phase does — chunk
	// refs live inside content objects and cannot be named yet.
	release := reader.DeclareDemand(contentKeys, store.DemandFinal)
	defer release()

	// Interleave the undeclared chunk reads with the declared content reads,
	// which is what actually happens: a file's content object is read, then the
	// chunks it names.
	for i := range contentKeys {
		if _, err := reader.Get(ctx, contentKeys[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Get(ctx, chunkKeys[i]); err != nil {
			t.Fatal(err)
		}
		if i < len(contentKeys)-1 && reader.packCache.byteLen() == 0 {
			t.Fatalf("pack released after %d of %d declared content reads; a chunk read retired content demand",
				i+1, len(contentKeys))
		}
	}
}

// The chain walker has to find PackStore under the decorators the real client
// stacks on it, or the capability is dead in production while every direct
// unit test passes.
func TestDeclareDemandReachesPackStoreThroughTheChain(t *testing.T) {
	const objects = packPromoteAfter * 2
	ctx, packed, counts, keys := newDemandFixture(t, objects)

	var chain store.ObjectStore = packed
	chain = NewMeteredStore(chain)
	chain = NewCompressedStore(chain)

	release := store.DeclareDemand(chain, keys, store.DemandFinal)
	defer release()

	packRef := packRefOf(t, packed, keys[0])
	if got := packed.demand.outstanding(packRef); got != objects {
		t.Fatalf("declaring through the wrapper chain reached PackStore with outstanding=%d, want %d", got, objects)
	}

	readAll(t, ctx, packed, keys)
	if counts.rangeGets != 0 {
		t.Errorf("declared through the chain but still probed %d times", counts.rangeGets)
	}
}

// A store with nothing to declare to must still hand back a callable release.
//
// Every call site spells this `defer store.DeclareDemand(...)()`, so the
// fallback returning nil would panic on a plain local backend — the
// configuration with the least to gain from demand counting and the most likely
// to be run without it.
func TestDeclareDemandWithoutADeclarerReturnsACallableRelease(t *testing.T) {
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Wrapped, so the helper walks a chain and finds no declarer at the bottom
	// rather than failing to walk at all.
	var chain store.ObjectStore = base
	chain = NewMeteredStore(chain)
	chain = NewCompressedStore(chain)

	release := store.DeclareDemand(chain, []string{"filemeta/a", "content/b"}, store.DemandFinal)
	if release == nil {
		t.Fatal("DeclareDemand returned a nil release; every call site defers it")
	}
	release()
	// Released twice on purpose: callers defer it and may also release early.
	release()
}
