package store_test

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

// zeroHintStore is a backend whose concurrency hint is zero — a plausible
// mistake for an interface exported to other modules, and one that must not be
// able to hang a caller.
type zeroHintStore struct {
	store.ObjectStore
}

func (zeroHintStore) ConcurrencyHint() int { return 0 }

// zeroPlanStore is a ReadPlanner that answers with a zero concurrency directly,
// covering the other way the value can arrive.
type zeroPlanStore struct {
	store.ObjectStore
}

func (zeroPlanStore) PlanReads(_ context.Context, keys []string) store.ReadPlan {
	return store.ReadPlan{Groups: [][]string{keys}, Concurrency: 0}
}

// Callers feed ReadPlan.Concurrency straight to errgroup.SetLimit, where zero
// means no goroutine may ever run — so the first submission blocks forever
// rather than falling back to something sensible. The guarantee has to hold
// whichever source produced the number, since a caller cannot tell which store
// answered.
func TestPlanReadsNeverReturnsUnusableConcurrency(t *testing.T) {
	ctx := context.Background()
	keys := []string{"chunk/a", "chunk/b"}

	for _, tc := range []struct {
		name  string
		store store.ObjectStore
	}{
		{"backend hint of zero", zeroHintStore{storetest.NewMemStore()}},
		{"planner answering zero", zeroPlanStore{storetest.NewMemStore()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.PlanReads(ctx, tc.store, keys).Concurrency; got < 1 {
				t.Errorf("Concurrency = %d, want at least 1: errgroup.SetLimit(%d) blocks the first g.Go forever", got, got)
			}
		})
	}
}

// A store with nothing to say still has to produce a plan a caller can run.
func TestPlanReadsFallbackIsUsable(t *testing.T) {
	plan := store.PlanReads(context.Background(), storetest.NewMemStore(), []string{"chunk/a", "chunk/b"})

	if plan.Concurrency < 1 {
		t.Errorf("fallback Concurrency = %d, want at least 1", plan.Concurrency)
	}
	if len(plan.Groups) != 2 {
		t.Errorf("fallback returned %d groups for 2 keys, want a singleton each: one group of everything serialises a caller that makes the group its unit of concurrency", len(plan.Groups))
	}
}
