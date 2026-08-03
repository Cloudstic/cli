package core

import (
	"encoding/json"
	"testing"
)

// The types below are the v1.18.0 shapes of the repository marker and the
// snapshot catalog entry — the last release before RepoConfig.ID and
// SnapshotSummary.CopiedFrom existed. They are copied here verbatim rather than
// derived, because the point is to detect a change on this side: if a future
// edit to the live struct makes older builds misread a repository, that is a
// version-gate decision (docs/compatibility.md), and this test is where the
// question gets asked.
//
// Forward compatibility is not guaranteed in general, but failure must be safe.
// For these two additions the bar is higher than "safe": an older build must
// read the objects exactly as it did before, because both fields were added
// without raising core.RepoFormatVersion. See RFC 0017 "Compatibility".

type repoConfigV1_18 struct {
	Version   int    `json:"version"`
	Created   string `json:"created"`
	Encrypted bool   `json:"encrypted"`
}

type snapshotSummaryV1_18 struct {
	Ref         string      `json:"ref"`
	Seq         int         `json:"seq"`
	Created     string      `json:"created"`
	Root        string      `json:"root"`
	Source      *SourceInfo `json:"source,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	ChangeToken string      `json:"change_token,omitempty"`
	ExcludeHash string      `json:"exclude_hash,omitempty"`
}

func TestRepoConfigWithIDIsReadableByV1_18(t *testing.T) {
	current, err := json.Marshal(RepoConfig{
		Version:   RepoFormatVersion,
		Created:   "2026-08-03T00:00:00Z",
		Encrypted: true,
		ID:        "9ac3fa64fc86a301a168b78d67a5b3cf",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var old repoConfigV1_18
	if err := json.Unmarshal(current, &old); err != nil {
		t.Fatalf("v1.18.0 could not decode a marker carrying an id: %v", err)
	}
	if old.Version != RepoFormatVersion {
		t.Errorf("version = %d, want %d", old.Version, RepoFormatVersion)
	}
	if old.Created != "2026-08-03T00:00:00Z" {
		t.Errorf("created = %q", old.Created)
	}
	if !old.Encrypted {
		t.Error("encrypted flag was lost")
	}
}

func TestSnapshotSummaryWithProvenanceIsReadableByV1_18(t *testing.T) {
	prov := CopyProvenance{RepoID: "9ac3fa64", SnapshotRef: "snapshot/abc123"}
	current, err := json.Marshal(SnapshotSummary{
		Ref:         "snapshot/def456",
		Seq:         7,
		Created:     "2026-08-03T00:00:00Z",
		Root:        "node/aaa",
		Tags:        []string{"workstation"},
		ExcludeHash: "hash",
		CopiedFrom:  prov.String(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var old snapshotSummaryV1_18
	if err := json.Unmarshal(current, &old); err != nil {
		t.Fatalf("v1.18.0 could not decode a catalog entry carrying provenance: %v", err)
	}
	if old.Ref != "snapshot/def456" || old.Seq != 7 || old.Root != "node/aaa" {
		t.Errorf("entry decoded wrong: %+v", old)
	}
	if old.ExcludeHash != "hash" || len(old.Tags) != 1 || old.Tags[0] != "workstation" {
		t.Errorf("entry decoded wrong: %+v", old)
	}
}

// An older build that rebuilds the catalog writes entries back without the
// field. That must degrade to "provenance unknown" rather than to a value this
// build would mistake for a match — the difference between copy re-importing a
// snapshot (wasteful) and copy skipping one it never copied (a silent hole).
func TestCatalogEntryWrittenByV1_18DecodesAsUnknownProvenance(t *testing.T) {
	oldBytes, err := json.Marshal(snapshotSummaryV1_18{
		Ref: "snapshot/def456", Seq: 7, Created: "2026-08-03T00:00:00Z", Root: "node/aaa",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var current SnapshotSummary
	if err := json.Unmarshal(oldBytes, &current); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if current.CopiedFrom != "" {
		t.Fatalf("CopiedFrom = %q, want empty", current.CopiedFrom)
	}
	if _, ok := ParseCopyProvenance(current.CopiedFrom); ok {
		t.Error("an absent provenance parsed as a match")
	}
}
