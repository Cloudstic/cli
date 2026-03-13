package engine

import (
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/core"
)

func makeEntry(ref, created string, source *core.SourceInfo, tags []string) SnapshotEntry {
	t, _ := time.Parse(time.RFC3339, created)
	return SnapshotEntry{
		Ref:     "snapshot/" + ref,
		Snap:    core.Snapshot{Created: created, Source: source, Tags: tags},
		Created: t,
	}
}

func TestApplyPolicy_KeepLast(t *testing.T) {
	entries := []SnapshotEntry{
		makeEntry("a", "2024-01-05T12:00:00Z", nil, nil),
		makeEntry("b", "2024-01-04T12:00:00Z", nil, nil),
		makeEntry("c", "2024-01-03T12:00:00Z", nil, nil),
		makeEntry("d", "2024-01-02T12:00:00Z", nil, nil),
		makeEntry("e", "2024-01-01T12:00:00Z", nil, nil),
	}

	keep, remove := applyPolicy(entries, ForgetPolicy{KeepLast: 3})

	if len(keep) != 3 {
		t.Errorf("expected 3 kept, got %d", len(keep))
	}
	if len(remove) != 2 {
		t.Errorf("expected 2 removed, got %d", len(remove))
	}
	if keep[0].Entry.Ref != "snapshot/a" {
		t.Errorf("expected newest kept first, got %s", keep[0].Entry.Ref)
	}
}

func TestApplyPolicy_KeepDaily(t *testing.T) {
	entries := []SnapshotEntry{
		makeEntry("a", "2024-01-03T15:00:00Z", nil, nil),
		makeEntry("b", "2024-01-03T10:00:00Z", nil, nil),
		makeEntry("c", "2024-01-02T15:00:00Z", nil, nil),
		makeEntry("d", "2024-01-02T10:00:00Z", nil, nil),
		makeEntry("e", "2024-01-01T15:00:00Z", nil, nil),
	}

	keep, remove := applyPolicy(entries, ForgetPolicy{KeepDaily: 2})

	if len(keep) != 2 {
		t.Errorf("expected 2 kept, got %d", len(keep))
	}
	if len(remove) != 3 {
		t.Errorf("expected 3 removed, got %d", len(remove))
	}
	if keep[0].Entry.Ref != "snapshot/a" {
		t.Errorf("expected Jan 3 15:00 kept, got %s", keep[0].Entry.Ref)
	}
	if keep[1].Entry.Ref != "snapshot/c" {
		t.Errorf("expected Jan 2 15:00 kept, got %s", keep[1].Entry.Ref)
	}
}

func TestApplyPolicy_KeepWeekly(t *testing.T) {
	entries := []SnapshotEntry{
		makeEntry("a", "2024-01-14T12:00:00Z", nil, nil), // week 2
		makeEntry("b", "2024-01-13T12:00:00Z", nil, nil), // week 2
		makeEntry("c", "2024-01-07T12:00:00Z", nil, nil), // week 1
		makeEntry("d", "2024-01-01T12:00:00Z", nil, nil), // week 1
	}

	keep, remove := applyPolicy(entries, ForgetPolicy{KeepWeekly: 2})

	if len(keep) != 2 {
		t.Errorf("expected 2 kept, got %d", len(keep))
	}
	if len(remove) != 2 {
		t.Errorf("expected 2 removed, got %d", len(remove))
	}
}

func TestApplyPolicy_Combined(t *testing.T) {
	entries := []SnapshotEntry{
		makeEntry("a", "2024-01-05T12:00:00Z", nil, nil),
		makeEntry("b", "2024-01-04T12:00:00Z", nil, nil),
		makeEntry("c", "2024-01-03T12:00:00Z", nil, nil),
		makeEntry("d", "2024-01-02T12:00:00Z", nil, nil),
		makeEntry("e", "2024-01-01T12:00:00Z", nil, nil),
	}

	// keep-last 1 + keep-daily 3 = snapshot a (last + daily), b (daily), c (daily)
	keep, remove := applyPolicy(entries, ForgetPolicy{KeepLast: 1, KeepDaily: 3})

	if len(keep) != 3 {
		t.Errorf("expected 3 kept, got %d", len(keep))
	}
	if len(remove) != 2 {
		t.Errorf("expected 2 removed, got %d", len(remove))
	}

	// The newest should have both "last" and "daily snapshot" reasons.
	if len(keep[0].Reasons) < 2 {
		t.Errorf("expected at least 2 reasons for newest, got %v", keep[0].Reasons)
	}
}

func TestApplyPolicy_EmptyPolicy(t *testing.T) {
	entries := []SnapshotEntry{
		makeEntry("a", "2024-01-01T12:00:00Z", nil, nil),
	}

	keep, remove := applyPolicy(entries, ForgetPolicy{})

	if len(keep) != 0 {
		t.Errorf("expected 0 kept with empty policy, got %d", len(keep))
	}
	if len(remove) != 1 {
		t.Errorf("expected 1 removed, got %d", len(remove))
	}
}

func TestGroupSnapshots(t *testing.T) {
	gdrive := &core.SourceInfo{Type: "gdrive", Account: "user@gmail.com"}
	local := &core.SourceInfo{Type: "local", Account: "myhost", Path: "/data"}

	entries := []SnapshotEntry{
		makeEntry("a", "2024-01-03T12:00:00Z", gdrive, nil),
		makeEntry("b", "2024-01-02T12:00:00Z", gdrive, nil),
		makeEntry("c", "2024-01-01T12:00:00Z", local, nil),
	}

	groups := groupSnapshots(entries, defaultGroupFields())
	if len(groups) != 2 {
		t.Errorf("expected 2 groups, got %d", len(groups))
	}
}

func TestMatchesFilter(t *testing.T) {
	gdrive := &core.SourceInfo{Type: "gdrive", Account: "user@gmail.com"}

	snap := core.Snapshot{Source: gdrive, Tags: []string{"daily", "important"}}

	if !matchesFilter(&snap, snapshotFilter{}) {
		t.Error("empty filter should match everything")
	}
	if !matchesFilter(&snap, snapshotFilter{source: "gdrive"}) {
		t.Error("should match source gdrive")
	}
	if matchesFilter(&snap, snapshotFilter{source: "local"}) {
		t.Error("should not match source local")
	}
	if !matchesFilter(&snap, snapshotFilter{tags: []string{"daily"}}) {
		t.Error("should match tag daily")
	}
	if matchesFilter(&snap, snapshotFilter{tags: []string{"daily", "missing"}}) {
		t.Error("should not match when a tag is missing")
	}
}

// TestMatchesFilter_VolumeUUID verifies that filtering by -account accepts
// VolumeUUID as an alternative to the hostname, so that portable-drive
// snapshots can be targeted by their stable UUID.
func TestMatchesFilter_VolumeUUID(t *testing.T) {
	const uuid = "A1B2C3D4-1234-5678-ABCD-EF0123456789"

	portable := &core.SourceInfo{
		Type:       "local",
		Account:    "macbook-pro", // hostname on machine A
		Path:       "Documents",
		VolumeUUID: uuid,
	}
	snap := core.Snapshot{Source: portable}

	// Filtering by VolumeUUID should match.
	if !matchesFilter(&snap, snapshotFilter{account: uuid}) {
		t.Error("should match when account filter equals VolumeUUID")
	}
	// Filtering by hostname still works.
	if !matchesFilter(&snap, snapshotFilter{account: "macbook-pro"}) {
		t.Error("should still match when account filter equals hostname")
	}
	// A different UUID or hostname should not match.
	if matchesFilter(&snap, snapshotFilter{account: "OTHER-UUID"}) {
		t.Error("should not match a different UUID")
	}
}

// TestGroupSnapshots_VolumeUUID verifies that snapshots from different
// machines but the same VolumeUUID are grouped together.
func TestGroupSnapshots_VolumeUUID(t *testing.T) {
	macSource := &core.SourceInfo{
		Type:       "local",
		Account:    "mac-studio.local",
		Path:       ".",
		VolumeUUID: "UUID-SHARED",
	}
	linuxSource := &core.SourceInfo{
		Type:       "local",
		Account:    "linux-workstation",
		Path:       ".",
		VolumeUUID: "UUID-SHARED",
	}
	otherSource := &core.SourceInfo{
		Type:       "local",
		Account:    "mac-studio.local",
		Path:       ".",
		VolumeUUID: "UUID-OTHER",
	}

	entries := []SnapshotEntry{
		makeEntry("a", "2026-03-03T12:00:00Z", macSource, nil),
		makeEntry("b", "2026-03-02T12:00:00Z", linuxSource, nil),
		makeEntry("c", "2026-03-01T12:00:00Z", otherSource, nil),
	}

	groups := groupSnapshots(entries, defaultGroupFields())

	// macSource and linuxSource should be in the same group (same UUID + path).
	// otherSource should be separate (different UUID).
	if len(groups) != 2 {
		t.Errorf("expected 2 groups (same UUID grouped together), got %d", len(groups))
		for k, v := range groups {
			t.Logf("  group %v: %d entries", k, len(v))
		}
	}

	// Find the group with 2 entries (the shared UUID group).
	found := false
	for _, v := range groups {
		if len(v) == 2 {
			found = true
		}
	}
	if !found {
		t.Error("expected one group with 2 entries (same VolumeUUID)")
	}
}

// TestGroupSnapshots_VolumeUUID_DifferentSubdirs verifies that backups of
// different sub-directories on the same drive are grouped independently.
func TestGroupSnapshots_VolumeUUID_DifferentSubdirs(t *testing.T) {
	photosSource := &core.SourceInfo{
		Type:       "local",
		Account:    "mac-studio.local",
		Path:       "Photos",
		VolumeUUID: "UUID-SHARED",
	}
	docsSource := &core.SourceInfo{
		Type:       "local",
		Account:    "mac-studio.local",
		Path:       "Documents",
		VolumeUUID: "UUID-SHARED",
	}

	entries := []SnapshotEntry{
		makeEntry("a", "2026-03-02T12:00:00Z", photosSource, nil),
		makeEntry("b", "2026-03-01T12:00:00Z", docsSource, nil),
	}

	groups := groupSnapshots(entries, defaultGroupFields())
	if len(groups) != 2 {
		t.Errorf("expected 2 groups (same UUID, different paths), got %d", len(groups))
	}
}

// TestGroupSnapshots_MixedUUIDAndLegacy verifies that snapshots with
// VolumeUUID group by UUID+path while those without group by account+path.
func TestGroupSnapshots_MixedUUIDAndLegacy(t *testing.T) {
	withUUID := &core.SourceInfo{
		Type:       "local",
		Account:    "mac-studio.local",
		Path:       ".",
		VolumeUUID: "UUID-1234",
	}
	withoutUUID := &core.SourceInfo{
		Type:    "local",
		Account: "mac-studio.local",
		Path:    "/data",
	}

	entries := []SnapshotEntry{
		makeEntry("a", "2026-03-02T12:00:00Z", withUUID, nil),
		makeEntry("b", "2026-03-01T12:00:00Z", withoutUUID, nil),
	}

	groups := groupSnapshots(entries, defaultGroupFields())
	if len(groups) != 2 {
		t.Errorf("expected 2 groups (UUID vs legacy), got %d", len(groups))
	}
}
