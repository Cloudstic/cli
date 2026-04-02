package engine

import (
	"context"
	"encoding/json"
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
