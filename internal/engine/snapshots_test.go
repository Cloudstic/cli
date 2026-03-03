package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/core"
)

// helper: store a snapshot object and return its ref.
func putSnapshot(t *testing.T, s *MockStore, snap *core.Snapshot) string {
	t.Helper()
	hash, data, err := core.ComputeJSONHash(snap)
	if err != nil {
		t.Fatalf("ComputeJSONHash: %v", err)
	}
	ref := "snapshot/" + hash
	if err := s.Put(context.Background(), ref, data); err != nil {
		t.Fatalf("Put %s: %v", ref, err)
	}
	return ref
}

// helper: store a catalog directly.
func putCatalog(t *testing.T, s *MockStore, catalog []core.SnapshotSummary) {
	t.Helper()
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("Marshal catalog: %v", err)
	}
	if err := s.Put(context.Background(), snapshotCatalogKey, data); err != nil {
		t.Fatalf("Put catalog: %v", err)
	}
}

// helper: read catalog from store.
func readCatalog(t *testing.T, s *MockStore) []core.SnapshotSummary {
	t.Helper()
	data, err := s.Get(context.Background(), snapshotCatalogKey)
	if err != nil {
		t.Fatalf("Get catalog: %v", err)
	}
	var catalog []core.SnapshotSummary
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("Unmarshal catalog: %v", err)
	}
	return catalog
}

func TestLoadSnapshotCatalog_NoCatalog(t *testing.T) {
	s := NewMockStore()
	now := time.Now()

	snap1 := &core.Snapshot{Seq: 1, Created: now.Add(-2 * time.Hour).Format(time.RFC3339), Root: "node/a"}
	snap2 := &core.Snapshot{Seq: 2, Created: now.Add(-1 * time.Hour).Format(time.RFC3339), Root: "node/b"}
	ref1 := putSnapshot(t, s, snap1)
	ref2 := putSnapshot(t, s, snap2)

	entries, err := LoadSnapshotCatalog(s)
	if err != nil {
		t.Fatalf("LoadSnapshotCatalog: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Should be sorted newest-first.
	if entries[0].Ref != ref2 {
		t.Errorf("entries[0].Ref = %s, want %s", entries[0].Ref, ref2)
	}
	if entries[1].Ref != ref1 {
		t.Errorf("entries[1].Ref = %s, want %s", entries[1].Ref, ref1)
	}

	// Catalog should have been persisted.
	catalog := readCatalog(t, s)
	if len(catalog) != 2 {
		t.Errorf("persisted catalog has %d entries, want 2", len(catalog))
	}
}

func TestLoadSnapshotCatalog_WithValidCatalog(t *testing.T) {
	s := NewMockStore()
	now := time.Now()

	snap1 := &core.Snapshot{Seq: 1, Created: now.Format(time.RFC3339), Root: "node/a",
		Source: &core.SourceInfo{Type: "local", Path: "/data"}}
	ref1 := putSnapshot(t, s, snap1)

	// Pre-populate catalog.
	putCatalog(t, s, []core.SnapshotSummary{
		snapshotToSummary(ref1, *snap1),
	})

	entries, err := LoadSnapshotCatalog(s)
	if err != nil {
		t.Fatalf("LoadSnapshotCatalog: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Snap.Source == nil || entries[0].Snap.Source.Type != "local" {
		t.Errorf("source not preserved from catalog")
	}
}

func TestLoadSnapshotCatalog_ReconcilesMissingEntries(t *testing.T) {
	s := NewMockStore()
	now := time.Now()

	snap1 := &core.Snapshot{Seq: 1, Created: now.Add(-1 * time.Hour).Format(time.RFC3339), Root: "node/a"}
	snap2 := &core.Snapshot{Seq: 2, Created: now.Format(time.RFC3339), Root: "node/b"}
	ref1 := putSnapshot(t, s, snap1)
	ref2 := putSnapshot(t, s, snap2)

	// Catalog only knows about snap1.
	putCatalog(t, s, []core.SnapshotSummary{
		snapshotToSummary(ref1, *snap1),
	})

	entries, err := LoadSnapshotCatalog(s)
	if err != nil {
		t.Fatalf("LoadSnapshotCatalog: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Catalog should now contain both.
	catalog := readCatalog(t, s)
	if len(catalog) != 2 {
		t.Errorf("rebuilt catalog has %d entries, want 2", len(catalog))
	}

	_ = ref2
}

func TestLoadSnapshotCatalog_RemovesStaleEntries(t *testing.T) {
	s := NewMockStore()
	now := time.Now()

	snap1 := &core.Snapshot{Seq: 1, Created: now.Format(time.RFC3339), Root: "node/a"}
	ref1 := putSnapshot(t, s, snap1)

	// Catalog has an extra stale entry.
	putCatalog(t, s, []core.SnapshotSummary{
		snapshotToSummary(ref1, *snap1),
		{Ref: "snapshot/deleted", Seq: 99, Created: now.Format(time.RFC3339), Root: "node/gone"},
	})

	entries, err := LoadSnapshotCatalog(s)
	if err != nil {
		t.Fatalf("LoadSnapshotCatalog: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Ref != ref1 {
		t.Errorf("expected ref %s, got %s", ref1, entries[0].Ref)
	}

	catalog := readCatalog(t, s)
	if len(catalog) != 1 {
		t.Errorf("rebuilt catalog has %d entries, want 1", len(catalog))
	}
}

func TestAppendSnapshotCatalog(t *testing.T) {
	s := NewMockStore()
	now := time.Now()

	snap1 := &core.Snapshot{Seq: 1, Created: now.Format(time.RFC3339), Root: "node/a"}

	// Start with empty catalog.
	AppendSnapshotCatalog(s, snapshotToSummary("snapshot/abc", *snap1))

	catalog := readCatalog(t, s)
	if len(catalog) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(catalog))
	}
	if catalog[0].Ref != "snapshot/abc" {
		t.Errorf("ref = %s, want snapshot/abc", catalog[0].Ref)
	}

	// Append a second.
	snap2 := &core.Snapshot{Seq: 2, Created: now.Format(time.RFC3339), Root: "node/b"}
	AppendSnapshotCatalog(s, snapshotToSummary("snapshot/def", *snap2))

	catalog = readCatalog(t, s)
	if len(catalog) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(catalog))
	}
}

func TestRemoveFromSnapshotCatalog(t *testing.T) {
	s := NewMockStore()
	now := time.Now()

	putCatalog(t, s, []core.SnapshotSummary{
		{Ref: "snapshot/aaa", Seq: 1, Created: now.Format(time.RFC3339)},
		{Ref: "snapshot/bbb", Seq: 2, Created: now.Format(time.RFC3339)},
		{Ref: "snapshot/ccc", Seq: 3, Created: now.Format(time.RFC3339)},
	})

	RemoveFromSnapshotCatalog(s, "snapshot/aaa", "snapshot/ccc")

	catalog := readCatalog(t, s)
	if len(catalog) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(catalog))
	}
	if catalog[0].Ref != "snapshot/bbb" {
		t.Errorf("remaining ref = %s, want snapshot/bbb", catalog[0].Ref)
	}
}

func TestRemoveFromSnapshotCatalog_NoCatalog(t *testing.T) {
	s := NewMockStore()

	// Should not panic when catalog doesn't exist.
	RemoveFromSnapshotCatalog(s, "snapshot/xxx")
}

func TestSnapshotToSummary(t *testing.T) {
	src := &core.SourceInfo{Type: "gdrive", Account: "user@example.com", Path: "root-id"}
	snap := core.Snapshot{
		Version:     1,
		Seq:         5,
		Created:     "2025-01-01T00:00:00Z",
		Root:        "node/abc",
		Source:      src,
		Tags:        []string{"daily"},
		ChangeToken: "token123",
	}

	summary := snapshotToSummary("snapshot/xyz", snap)

	if summary.Ref != "snapshot/xyz" {
		t.Errorf("Ref = %s", summary.Ref)
	}
	if summary.Seq != 5 {
		t.Errorf("Seq = %d", summary.Seq)
	}
	if summary.Root != "node/abc" {
		t.Errorf("Root = %s", summary.Root)
	}
	if summary.Source == nil || summary.Source.Type != "gdrive" {
		t.Error("Source not preserved")
	}
	if len(summary.Tags) != 1 || summary.Tags[0] != "daily" {
		t.Errorf("Tags = %v", summary.Tags)
	}
	if summary.ChangeToken != "token123" {
		t.Errorf("ChangeToken = %s", summary.ChangeToken)
	}
}

func TestLoadSnapshotCatalog_EmptyRepo(t *testing.T) {
	s := NewMockStore()

	entries, err := LoadSnapshotCatalog(s)
	if err != nil {
		t.Fatalf("LoadSnapshotCatalog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}
