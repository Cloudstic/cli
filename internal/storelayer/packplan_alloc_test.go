package storelayer

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/pkg/store/storetest"
)

// catalogOfSize returns a store whose catalog holds entries objects spread over
// packs of 2,000, loaded so PlanReads will not try to read one.
func catalogOfSize(t testing.TB, entries int) *PackStore {
	t.Helper()
	s, err := NewPackStore(storetest.NewMemStore())
	if err != nil {
		t.Fatalf("NewPackStore: %v", err)
	}
	for i := range entries {
		s.catalog.Set(fmt.Sprintf("chunk/%064x", i), PackEntry{
			PackRef: fmt.Sprintf("packs/%064x", i/2000),
			Offset:  int64(i % 2000 * 4096),
			Length:  4096,
		})
	}
	s.catalogLoaded = true
	return s
}

func planKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("chunk/%064x", i)
	}
	return keys
}

// Planning a fixed set of keys must cost the same whatever else the repository
// holds.
//
// It did not. Admission needs each pack's total size, and that was computed by
// scanning the whole catalog — which also rebuilds every entry's key string,
// three allocations apiece, all discarded. A 512-key request against a
// 200,000-entry catalog allocated 41.6 MB and 600,013 times. Every planning
// caller paid it; `backup` merely made it visible by planning once per batch,
// so its cost grew with the square of the file count.
//
// The assertion is on the *ratio* rather than an absolute figure: what has to
// hold is that the cost does not track the repository, and pinning a byte count
// would fail on any unrelated change to the plan's own allocations.
func TestPlanReadsDoesNotAllocatePerCatalogEntry(t *testing.T) {
	ctx := context.Background()
	keys := planKeys(512)

	measure := func(entries int) float64 {
		s := catalogOfSize(t, entries)
		// Warm any one-time work so it is not attributed to the measured runs.
		s.PlanReads(ctx, keys)
		return testing.AllocsPerRun(5, func() { s.PlanReads(ctx, keys) })
	}

	small := measure(2_000)
	large := measure(100_000)

	// A 50x larger catalog must not cost meaningfully more. The allowance is for
	// map-sizing effects in the plan itself, not for per-entry work.
	if large > small*1.5+16 {
		t.Errorf("PlanReads allocated %.0f times against a 100,000-entry catalog and %.0f against a 2,000-entry one, "+
			"for the same 512 keys: planning is scaling with the repository", large, small)
	}
}

// The extent is what admission divides by, so it has to stay right as entries
// are added out of order and across packs.
func TestPackCatalogPackExtent(t *testing.T) {
	c := newPackCatalog()
	c.Set("chunk/"+fmt.Sprintf("%064x", 1), PackEntry{PackRef: "packs/a", Offset: 4096, Length: 100})
	c.Set("chunk/"+fmt.Sprintf("%064x", 2), PackEntry{PackRef: "packs/a", Offset: 0, Length: 4096})
	c.Set("chunk/"+fmt.Sprintf("%064x", 3), PackEntry{PackRef: "packs/b", Offset: 0, Length: 7})

	if got, ok := c.PackExtent("packs/a"); !ok || got != 4196 {
		t.Errorf("PackExtent(packs/a) = %d, %v; want 4196, true — the furthest end, not the last written", got, ok)
	}
	if got, ok := c.PackExtent("packs/b"); !ok || got != 7 {
		t.Errorf("PackExtent(packs/b) = %d, %v; want 7, true", got, ok)
	}
	if _, ok := c.PackExtent("packs/missing"); ok {
		t.Error("PackExtent reported a pack the catalog never saw")
	}
}

// Deleting an entry must not shrink the extent. The bytes stay in the stored
// packfile until Repack rewrites it, so what fetching that pack would transfer
// is unchanged — and admission is deciding exactly that.
func TestPackCatalogPackExtentSurvivesDelete(t *testing.T) {
	c := newPackCatalog()
	last := "chunk/" + fmt.Sprintf("%064x", 9)
	c.Set("chunk/"+fmt.Sprintf("%064x", 8), PackEntry{PackRef: "packs/a", Offset: 0, Length: 4096})
	c.Set(last, PackEntry{PackRef: "packs/a", Offset: 4096, Length: 4096})

	c.Delete(last)

	if got, _ := c.PackExtent("packs/a"); got != 8192 {
		t.Errorf("PackExtent after deleting the last entry = %d, want 8192: the pack is still that large on the store", got)
	}
}

func BenchmarkPlanReadsByCatalogSize(b *testing.B) {
	ctx := context.Background()
	keys := planKeys(512)
	for _, entries := range []int{10_000, 50_000, 200_000} {
		b.Run(fmt.Sprint(entries), func(b *testing.B) {
			s := catalogOfSize(b, entries)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				s.PlanReads(ctx, keys)
			}
		})
	}
}
