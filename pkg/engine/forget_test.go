package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/ui"
)

func TestForgetManager_Run(t *testing.T) {
	store := NewMockStore()

	// Setup: Create 3 snapshots, latest points to snap3.
	snap1 := core.Snapshot{Seq: 1, Root: "node/1"}
	snap1Ref := saveSnapshot(store, &snap1)

	snap2 := core.Snapshot{Seq: 2, Root: "node/2"}
	snap2Ref := saveSnapshot(store, &snap2)

	snap3 := core.Snapshot{Seq: 3, Root: "node/3"}
	snap3Ref := saveSnapshot(store, &snap3)

	_ = store.Put("index/latest", createIndex(snap3Ref, 3))

	fm := NewForgetManager(store, ui.NewNoOpReporter())

	// Test 1: Forget intermediate snapshot (snap2) -- latest should not change.
	if _, err := fm.Run(context.Background(), snap2Ref); err != nil {
		t.Fatalf("Forget snap2 failed: %v", err)
	}

	assertNotExists(t, store, snap2Ref)
	assertExists(t, store, snap1Ref)
	assertExists(t, store, snap3Ref)
	assertExists(t, store, "index/latest")

	idxData, _ := store.Get("index/latest")
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

	assertNotExists(t, store, snap3Ref)

	idxData, _ = store.Get("index/latest")
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

func saveSnapshot(s *MockStore, snap *core.Snapshot) string {
	hash, data, _ := core.ComputeJSONHash(snap)
	ref := "snapshot/" + hash
	_ = s.Put(ref, data)
	return ref
}

func createIndex(ref string, seq int) []byte {
	idx := core.Index{LatestSnapshot: ref, Seq: seq}
	data, _ := json.Marshal(idx)
	return data
}
