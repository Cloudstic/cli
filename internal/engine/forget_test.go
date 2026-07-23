package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
)

func TestForgetManager_Run(t *testing.T) {
	ctx := context.Background()
	store := NewMockStore()

	// Setup: Create 3 snapshots, latest points to snap3.
	snap1 := core.Snapshot{Seq: 1, Root: "node/1"}
	snap1Ref := saveSnapshot(ctx, store, &snap1)

	snap2 := core.Snapshot{Seq: 2, Root: "node/2"}
	snap2Ref := saveSnapshot(ctx, store, &snap2)

	snap3 := core.Snapshot{Seq: 3, Root: "node/3"}
	snap3Ref := saveSnapshot(ctx, store, &snap3)

	_ = store.Put(ctx, "index/latest", createIndex(snap3Ref, 3))

	fm := NewForgetManager(store, ui.NewNoOpReporter())

	// Test 1: Forget intermediate snapshot (snap2) -- latest should not change.
	if _, err := fm.Run(context.Background(), snap2Ref); err != nil {
		t.Fatalf("Forget snap2 failed: %v", err)
	}

	assertNotExists(t, ctx, store, snap2Ref)
	assertExists(t, ctx, store, snap1Ref)
	assertExists(t, ctx, store, snap3Ref)
	assertExists(t, ctx, store, "index/latest")

	idxData, _ := store.Get(ctx, "index/latest")
	var idx core.Index
	if err := json.Unmarshal(idxData, &idx); err != nil {
		t.Fatalf("Unmarshal index: %v", err)
	}
	if idx.LatestSnapshot != snap3Ref {
		t.Errorf("Latest snapshot changed unexpectedly: %s", idx.LatestSnapshot)
	}

	// Test 2: Forget latest snapshot (snap3) -- latest should fall back to snap1.
	if _, err := fm.Run(context.Background(), "latest"); err != nil {
		t.Fatalf("Forget latest failed: %v", err)
	}

	assertNotExists(t, ctx, store, snap3Ref)

	idxData, _ = store.Get(ctx, "index/latest")
	if err := json.Unmarshal(idxData, &idx); err != nil {
		t.Fatalf("Unmarshal index: %v", err)
	}
	if idx.LatestSnapshot != snap1Ref {
		t.Errorf("Latest snapshot should be snap1, got %s", idx.LatestSnapshot)
	}
	if idx.Seq != 1 {
		t.Errorf("Latest seq should be 1, got %d", idx.Seq)
	}
}

// A real backend failure while resolving "latest" must propagate rather than
// be reported as "no latest snapshot found", which would be misleading.
func TestForgetManager_ResolveSnapshot_PropagatesLatestError(t *testing.T) {
	backendErr := errors.New("simulated network error")
	s := newErrorOnGetStore(NewMockStore(), backendErr, "index/latest")
	fm := NewForgetManager(s, ui.NewNoOpReporter())

	_, err := fm.resolveSnapshot("latest")
	if err == nil {
		t.Fatal("expected resolveSnapshot(latest) to propagate a real backend error")
	}
	if !errors.Is(err, backendErr) {
		t.Errorf("resolveSnapshot error = %v, want it to wrap %v", err, backendErr)
	}
}

// A real backend failure while re-reading index/latest during fixupLatest
// must abort rather than be silently treated as "not the deleted ref" —
// otherwise index/latest could keep pointing at a snapshot that was just
// deleted.
func TestForgetManager_FixupLatest_PropagatesError(t *testing.T) {
	backendErr := errors.New("simulated network error")
	inner := NewMockStore()
	ctx := context.Background()
	snap := core.Snapshot{Seq: 1, Root: "node/1"}
	ref := saveSnapshot(ctx, inner, &snap)
	_ = inner.Put(ctx, "index/latest", createIndex(ref, 1))

	s := newErrorOnGetStore(inner, backendErr, "index/latest")
	fm := NewForgetManager(s, ui.NewNoOpReporter())

	err := fm.fixupLatest(ref)
	if err == nil {
		t.Fatal("expected fixupLatest to propagate a real backend error")
	}
	if !errors.Is(err, backendErr) {
		t.Errorf("fixupLatest error = %v, want it to wrap %v", err, backendErr)
	}
}

func saveSnapshot(ctx context.Context, s *MockStore, snap *core.Snapshot) string {
	hash, data, _ := core.ComputeJSONHash(snap)
	ref := "snapshot/" + hash
	_ = s.Put(ctx, ref, data)
	return ref
}

func createIndex(ref string, seq int) []byte {
	idx := core.Index{LatestSnapshot: ref, Seq: seq}
	data, _ := json.Marshal(idx)
	return data
}

func TestForgetManager_RunPolicy_RequiresPolicyOrFilter(t *testing.T) {
	fm := NewForgetManager(NewMockStore(), ui.NewNoOpReporter())

	_, err := fm.RunPolicy(context.Background())
	if err == nil {
		t.Fatal("expected error for empty policy and filter")
	}
	if got := err.Error(); got != "empty policy: specify at least one -keep-* option or a tag/source/account/path filter" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestForgetManager_RunPolicy_FilterOnlyAllowed(t *testing.T) {
	ctx := context.Background()
	store := NewMockStore()
	fm := NewForgetManager(store, ui.NewNoOpReporter())

	snap := core.Snapshot{
		Seq:     1,
		Root:    "node/1",
		Source:  &core.SourceInfo{Type: "local", Account: "host", Path: "/docs"},
		Tags:    []string{"daily"},
		Created: "2024-01-01T12:00:00Z",
	}
	ref := saveSnapshot(ctx, store, &snap)
	if err := store.Put(ctx, "index/latest", createIndex(ref, 1)); err != nil {
		t.Fatalf("put latest index: %v", err)
	}

	result, err := fm.RunPolicy(ctx, WithFilterTag("daily"))
	if err != nil {
		t.Fatalf("RunPolicy filter-only failed: %v", err)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result.Groups))
	}
	if len(result.Groups[0].Remove) != 1 {
		t.Fatalf("expected 1 snapshot to be removed by filter-only policy, got %d", len(result.Groups[0].Remove))
	}
}

// A dry run must resolve the snapshot and stop. Deleting anyway — which is what
// this did — turns a preview of a destructive command into the command itself,
// and the CLI even reported "Snapshot removed."
func TestForgetManager_DryRunRemovesNothing(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	snap := core.Snapshot{Seq: 1, Root: "node/1"}
	snapRef := saveSnapshot(ctx, s, &snap)
	_ = s.Put(ctx, "index/latest", createIndex(snapRef, 1))

	fm := NewForgetManager(s, ui.NewNoOpReporter())
	result, err := fm.Run(ctx, snapRef, WithDryRun())
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !result.DryRun {
		t.Error("the result should report that it was a dry run")
	}

	if exists, _ := s.Exists(ctx, snapRef); !exists {
		t.Error("a dry run deleted the snapshot")
	}

	// And without the flag it really goes.
	if _, err := fm.Run(ctx, snapRef); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if exists, _ := s.Exists(ctx, snapRef); exists {
		t.Error("a real forget left the snapshot behind")
	}
}
