package core

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestCopyProvenanceRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		prov CopyProvenance
		want string
	}{
		{
			name: "identified source",
			prov: CopyProvenance{RepoID: "9f2c1a", SnapshotRef: "snapshot/abc123"},
			want: "9f2c1a:snapshot/abc123",
		},
		{
			// A source predating RepoConfig.ID. This is a supported state, not
			// a degenerate one: matching falls back to the snapshot ref.
			name: "legacy source without an id",
			prov: CopyProvenance{SnapshotRef: "snapshot/abc123"},
			want: ":snapshot/abc123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.prov.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
			got, ok := ParseCopyProvenance(tc.want)
			if !ok {
				t.Fatalf("ParseCopyProvenance(%q) reported not-ok", tc.want)
			}
			if got != tc.prov {
				t.Errorf("round trip = %+v, want %+v", got, tc.prov)
			}
			if tc.prov.IsZero() {
				t.Error("IsZero() = true for a provenance carrying a snapshot ref")
			}
		})
	}
}

func TestCopyProvenanceZeroValue(t *testing.T) {
	var zero CopyProvenance
	if !zero.IsZero() {
		t.Error("IsZero() = false for the zero value")
	}
	if got := zero.String(); got != "" {
		t.Errorf("String() = %q, want empty so the catalog field stays omitted", got)
	}
}

func TestParseCopyProvenanceRejectsUnrecognized(t *testing.T) {
	// A catalog entry this build does not understand must not be mistaken for
	// a match, since a false match makes copy skip a snapshot it never copied.
	for _, in := range []string{"", "no-separator", "repo-with-empty-ref:"} {
		if _, ok := ParseCopyProvenance(in); ok {
			t.Errorf("ParseCopyProvenance(%q) accepted an unrecognized value", in)
		}
	}
}

func TestCopyProvenanceApplyToDoesNotMutateInput(t *testing.T) {
	// Copy passes the *source* snapshot's own Meta here while rewriting it,
	// so mutating the argument would corrupt the map it is still reading from.
	src := map[string]string{"generator": "cloudstic-cli"}
	prov := CopyProvenance{RepoID: "9f2c1a", SnapshotRef: "snapshot/abc123"}

	out := prov.ApplyTo(src)

	if len(src) != 1 || src["generator"] != "cloudstic-cli" {
		t.Fatalf("input map was modified: %+v", src)
	}
	if out["generator"] != "cloudstic-cli" {
		t.Error("existing metadata was not carried over")
	}
	if out[MetaKeyCopyFromRepo] != "9f2c1a" {
		t.Errorf("from_repo = %q, want 9f2c1a", out[MetaKeyCopyFromRepo])
	}
	if out[MetaKeyCopyFromSnapshot] != "snapshot/abc123" {
		t.Errorf("from_snapshot = %q, want snapshot/abc123", out[MetaKeyCopyFromSnapshot])
	}
}

func TestCopyProvenanceApplyToOmitsEmptyRepoID(t *testing.T) {
	// Recorded by omission rather than as "", so that a copy from a legacy
	// source cannot be confused with one from a source whose id really is "".
	out := CopyProvenance{SnapshotRef: "snapshot/abc123"}.ApplyTo(nil)

	if _, present := out[MetaKeyCopyFromRepo]; present {
		t.Error("from_repo key was written for a source with no id")
	}

	prov, ok := CopyProvenanceFromMeta(out)
	if !ok {
		t.Fatal("CopyProvenanceFromMeta did not recognize legacy provenance")
	}
	if prov.RepoID != "" || prov.SnapshotRef != "snapshot/abc123" {
		t.Errorf("read back %+v", prov)
	}
}

func TestCopyProvenanceApplyToClearsInheritedRepoID(t *testing.T) {
	// Re-stamping a snapshot that already carries provenance must not leave the
	// previous source's id behind next to the new snapshot ref.
	inherited := map[string]string{
		MetaKeyCopyFromRepo:     "old-repo",
		MetaKeyCopyFromSnapshot: "snapshot/old",
	}

	out := CopyProvenance{SnapshotRef: "snapshot/new"}.ApplyTo(inherited)

	if _, present := out[MetaKeyCopyFromRepo]; present {
		t.Errorf("stale from_repo survived: %q", out[MetaKeyCopyFromRepo])
	}
	if out[MetaKeyCopyFromSnapshot] != "snapshot/new" {
		t.Errorf("from_snapshot = %q, want snapshot/new", out[MetaKeyCopyFromSnapshot])
	}
}

func TestCopyProvenanceFromMetaIgnoresUncopiedSnapshots(t *testing.T) {
	for _, meta := range []map[string]string{
		nil,
		{},
		{"generator": "cloudstic-cli"},
		// A repo id with no snapshot ref is not provenance.
		{MetaKeyCopyFromRepo: "9f2c1a"},
	} {
		if _, ok := CopyProvenanceFromMeta(meta); ok {
			t.Errorf("CopyProvenanceFromMeta(%v) claimed provenance", meta)
		}
	}
}

func TestCopyProvenanceMatches(t *testing.T) {
	const ref = "snapshot/abc123"
	tests := []struct {
		name string
		a, b CopyProvenance
		want bool
		why  string
	}{
		{
			name: "same repository, same snapshot",
			a:    CopyProvenance{RepoID: "aaa", SnapshotRef: ref},
			b:    CopyProvenance{RepoID: "aaa", SnapshotRef: ref},
			want: true,
		},
		{
			name: "different snapshots in the same repository",
			a:    CopyProvenance{RepoID: "aaa", SnapshotRef: ref},
			b:    CopyProvenance{RepoID: "aaa", SnapshotRef: "snapshot/def456"},
			want: false,
		},
		{
			name: "same snapshot ref from two identified repositories",
			a:    CopyProvenance{RepoID: "aaa", SnapshotRef: ref},
			b:    CopyProvenance{RepoID: "bbb", SnapshotRef: ref},
			want: false,
			why:  "two known, different repositories are genuinely different sources",
		},
		{
			name: "id known on one side only",
			a:    CopyProvenance{RepoID: "aaa", SnapshotRef: ref},
			b:    CopyProvenance{SnapshotRef: ref},
			want: true,
			why:  "an older build drops the id when it rewrites the marker; unknown is not a mismatch",
		},
		{
			name: "id unknown on both sides",
			a:    CopyProvenance{SnapshotRef: ref},
			b:    CopyProvenance{SnapshotRef: ref},
			want: true,
			why:  "both sources predate RepoConfig.ID; the ref carries the match",
		},
		{
			name: "zero value never matches",
			a:    CopyProvenance{},
			b:    CopyProvenance{RepoID: "aaa", SnapshotRef: ref},
			want: false,
			why:  "a snapshot with no provenance was not copied from anywhere",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Matches(tc.b); got != tc.want {
				t.Errorf("Matches() = %v, want %v (%s)", got, tc.want, tc.why)
			}
			// Matching is a symmetric question and must not depend on which
			// side is the destination's record and which is the candidate.
			if got := tc.b.Matches(tc.a); got != tc.want {
				t.Errorf("reversed Matches() = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

func TestIsReservedMetaKey(t *testing.T) {
	reserved := []string{MetaKeyCopyFromRepo, MetaKeyCopyFromSnapshot, "cloudstic.anything"}
	for _, key := range reserved {
		if !IsReservedMetaKey(key) {
			t.Errorf("IsReservedMetaKey(%q) = false", key)
		}
	}
	for _, key := range []string{"generator", "host", "cloudstic", "my.cloudstic.key"} {
		if IsReservedMetaKey(key) {
			t.Errorf("IsReservedMetaKey(%q) = true, want false", key)
		}
	}
}

func TestNewRepoID(t *testing.T) {
	first, err := NewRepoID()
	if err != nil {
		t.Fatalf("NewRepoID: %v", err)
	}
	raw, err := hex.DecodeString(first)
	if err != nil {
		t.Fatalf("id %q is not hex: %v", first, err)
	}
	if len(raw) != RepoIDBytes {
		t.Errorf("decoded id is %d bytes, want %d", len(raw), RepoIDBytes)
	}

	second, err := NewRepoID()
	if err != nil {
		t.Fatalf("NewRepoID: %v", err)
	}
	if first == second {
		t.Error("two calls returned the same id")
	}
}

// The repository marker and the snapshot catalog gained fields without a format
// version bump, which is only sound if a value that does not use them encodes
// exactly as it did before. See RFC 0017 "Compatibility".
func TestAddedFieldsAreOmittedWhenUnset(t *testing.T) {
	cfg, err := json.Marshal(RepoConfig{Version: 2, Created: "2026-01-01T00:00:00Z", Encrypted: true})
	if err != nil {
		t.Fatalf("marshal RepoConfig: %v", err)
	}
	const wantCfg = `{"version":2,"created":"2026-01-01T00:00:00Z","encrypted":true}`
	if string(cfg) != wantCfg {
		t.Errorf("RepoConfig without an id encoded as\n  %s\nwant\n  %s", cfg, wantCfg)
	}

	summary, err := json.Marshal(SnapshotSummary{Ref: "snapshot/abc", Seq: 1, Created: "2026-01-01T00:00:00Z", Root: "node/def"})
	if err != nil {
		t.Fatalf("marshal SnapshotSummary: %v", err)
	}
	const wantSummary = `{"ref":"snapshot/abc","seq":1,"created":"2026-01-01T00:00:00Z","root":"node/def"}`
	if string(summary) != wantSummary {
		t.Errorf("SnapshotSummary without provenance encoded as\n  %s\nwant\n  %s", summary, wantSummary)
	}
}

// Provenance lives in Snapshot.Meta specifically so that recording it does not
// perturb the content-addressing of snapshots that do not carry it, and so that
// a snapshot that does carry it still hashes canonically regardless of the
// order the keys were inserted.
func TestProvenanceInMetaHashesCanonically(t *testing.T) {
	base := Snapshot{Version: 1, Created: "2026-01-01T00:00:00Z", Root: "node/def", Seq: 3}

	bare, _, err := ComputeJSONHash(&base)
	if err != nil {
		t.Fatalf("ComputeJSONHash: %v", err)
	}

	withProv := base
	withProv.Meta = CopyProvenance{RepoID: "9f2c1a", SnapshotRef: "snapshot/abc"}.ApplyTo(nil)
	first, _, err := ComputeJSONHash(&withProv)
	if err != nil {
		t.Fatalf("ComputeJSONHash: %v", err)
	}
	if first == bare {
		t.Fatal("recording provenance did not change the snapshot hash")
	}

	reordered := base
	reordered.Meta = map[string]string{
		MetaKeyCopyFromSnapshot: "snapshot/abc",
		MetaKeyCopyFromRepo:     "9f2c1a",
	}
	second, _, err := ComputeJSONHash(&reordered)
	if err != nil {
		t.Fatalf("ComputeJSONHash: %v", err)
	}
	if first != second {
		t.Errorf("insertion order changed the hash: %s vs %s", first, second)
	}
}
