package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
)

func copiedSnapshot(created string, prov core.CopyProvenance) *core.Snapshot {
	return &core.Snapshot{
		Version: 1,
		Created: created,
		Root:    "node/" + strings.Repeat("a", 8),
		Seq:     1,
		Meta:    prov.ApplyTo(map[string]string{"generator": "cloudstic-cli"}),
	}
}

func TestSnapshotToSummary_DerivesProvenance(t *testing.T) {
	prov := core.CopyProvenance{RepoID: "9f2c1a", SnapshotRef: "snapshot/source123"}
	summary := snapshotToSummary("snapshot/dest456", *copiedSnapshot("2026-01-01T00:00:00Z", prov))

	if summary.CopiedFrom != prov.String() {
		t.Errorf("CopiedFrom = %q, want %q", summary.CopiedFrom, prov.String())
	}
}

func TestSnapshotToSummary_LeavesProvenanceEmptyForOrdinarySnapshots(t *testing.T) {
	snap := core.Snapshot{Version: 1, Created: "2026-01-01T00:00:00Z", Meta: map[string]string{"generator": "cloudstic-cli"}}

	if got := snapshotToSummary("snapshot/abc", snap).CopiedFrom; got != "" {
		t.Errorf("CopiedFrom = %q for a snapshot that was not copied", got)
	}
}

// The catalog is a rebuildable cache, so a build predating CopiedFrom drops the
// field when it reconciles. The value must come back from the snapshot object's
// Meta on the next rebuild rather than staying lost — otherwise a single run of
// an older binary would make copy re-import the whole history.
func TestSnapshotCatalogLoad_RebuildsProvenanceFromSnapshotMeta(t *testing.T) {
	s := NewMockStore()
	prov := core.CopyProvenance{RepoID: "9f2c1a", SnapshotRef: "snapshot/source123"}
	ref := putSnapshot(t, s, copiedSnapshot("2026-01-01T00:00:00Z", prov))

	// A catalog as an older build would have left it: the entry is present and
	// otherwise correct, but carries no provenance.
	putCatalog(t, s, []core.SnapshotSummary{{
		Ref:     ref,
		Seq:     1,
		Created: "2026-01-01T00:00:00Z",
		Root:    "node/" + strings.Repeat("a", 8),
	}})

	entries, err := newSnapshotCatalog(s, nil).load()
	if err != nil {
		t.Fatalf("catalog load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	// The stale entry is served as-is; nothing forces a refetch, which is the
	// documented degraded state. What must hold is that a rebuild recovers it.
	rebuilt := snapshotToSummary(ref, *copiedSnapshot("2026-01-01T00:00:00Z", prov))
	if rebuilt.CopiedFrom != prov.String() {
		t.Errorf("rebuild produced CopiedFrom = %q, want %q", rebuilt.CopiedFrom, prov.String())
	}
}

// A snapshot written by backup lands in the catalog through the same funnel, so
// an ordinary backup must not start claiming provenance.
func TestSnapshotCatalogAdd_RoundTripsProvenance(t *testing.T) {
	s := NewMockStore()
	prov := core.CopyProvenance{RepoID: "9f2c1a", SnapshotRef: "snapshot/source123"}

	newSnapshotCatalog(s, nil).add(snapshotToSummary("snapshot/dest456", *copiedSnapshot("2026-01-01T00:00:00Z", prov)))
	newSnapshotCatalog(s, nil).add(snapshotToSummary("snapshot/plain", core.Snapshot{
		Version: 1, Created: "2026-01-02T00:00:00Z", Seq: 2,
	}))

	catalog := readCatalog(t, s)
	byRef := map[string]core.SnapshotSummary{}
	for _, entry := range catalog {
		byRef[entry.Ref] = entry
	}

	if got := byRef["snapshot/dest456"].CopiedFrom; got != prov.String() {
		t.Errorf("copied snapshot: CopiedFrom = %q, want %q", got, prov.String())
	}
	if got := byRef["snapshot/plain"].CopiedFrom; got != "" {
		t.Errorf("backed-up snapshot: CopiedFrom = %q, want empty", got)
	}
}

// Provenance is what copy trusts to decide it has already copied a snapshot, so
// a snapshot carrying a forged entry is one copy silently skips. The namespace
// is therefore closed at the point user metadata enters.
func TestBackup_RejectsReservedMetadataKeys(t *testing.T) {
	src := NewMockSource()
	mgr := NewBackupManager(src, NewMockStore(), ui.NewNoOpReporter(), nil, nil,
		WithMeta("host", "laptop"),
		WithMeta(core.MetaKeyCopyFromSnapshot, "snapshot/forged"),
	)

	_, err := mgr.Run(context.Background())
	if err == nil {
		t.Fatal("backup accepted a reserved metadata key")
	}
	if !strings.Contains(err.Error(), core.MetaKeyCopyFromSnapshot) {
		t.Errorf("error does not name the offending key: %v", err)
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error does not explain why the key was refused: %v", err)
	}
}

func TestBackup_AllowsOrdinaryMetadataKeys(t *testing.T) {
	src := NewMockSource()
	mgr := NewBackupManager(src, NewMockStore(), ui.NewNoOpReporter(), nil, nil,
		WithMeta("host", "laptop"),
		WithMeta("cloudstic", "not-the-prefix"),
	)

	if _, err := mgr.Run(context.Background()); err != nil {
		t.Fatalf("backup rejected ordinary metadata: %v", err)
	}
}
