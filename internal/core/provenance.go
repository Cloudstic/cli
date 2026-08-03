package core

import (
	"maps"
	"strings"
)

// Snapshot.Meta keys are user-controlled — `backup` sets them from -meta — with
// one exception: this prefix is reserved for values Cloudstic itself records
// about a snapshot. Without the reservation, provenance would be forgeable by
// anyone who can run a backup, and a forged entry makes `copy` skip a snapshot
// it never copied.
const ReservedMetaPrefix = "cloudstic."

// The provenance keys written by `copy` (RFC 0017 §5.2). Provenance lives in
// Meta rather than in new Snapshot fields because Snapshot is content-addressed
// through ComputeJSONHash, which marshals struct fields in declaration order —
// so a new field rewrites every snapshot hash, while a new map entry does not.
// encoding/json sorts map keys, so the encoding stays canonical.
const (
	MetaKeyCopyFromRepo     = ReservedMetaPrefix + "copy.from_repo"
	MetaKeyCopyFromSnapshot = ReservedMetaPrefix + "copy.from_snapshot"
)

// IsReservedMetaKey reports whether key belongs to Cloudstic rather than to the
// user. Callers accepting user-supplied metadata must reject these.
func IsReservedMetaKey(key string) bool {
	return strings.HasPrefix(key, ReservedMetaPrefix)
}

// CopyProvenance identifies the snapshot a copied snapshot was made from.
//
// RepoID is empty when the source repository predates RepoConfig.ID. That is a
// supported state, not an error: there is no stable identifier to derive for
// such a repository (see RepoConfig.ID), so matching falls back to SnapshotRef
// alone. Snapshot refs are content-addressed under the source master key, so
// for an encrypted source a collision across two repositories implies the two
// snapshots really are the same snapshot.
type CopyProvenance struct {
	RepoID      string
	SnapshotRef string // "snapshot/<hash>" in the *source* repository
}

// IsZero reports whether p records no provenance at all. A provenance with an
// empty RepoID but a set SnapshotRef is not zero — it is a legacy source.
func (p CopyProvenance) IsZero() bool { return p.SnapshotRef == "" }

// String renders p in the compact form stored in SnapshotSummary.CopiedFrom.
//
// Neither component can contain a colon — RepoID is hex and SnapshotRef is
// "snapshot/<hex>" — so the first colon always separates them.
func (p CopyProvenance) String() string {
	if p.IsZero() {
		return ""
	}
	return p.RepoID + ":" + p.SnapshotRef
}

// ParseCopyProvenance reverses String. It reports false for anything it does
// not recognise, so a catalog entry written by a future version cannot be
// mistaken for a match here.
func ParseCopyProvenance(s string) (CopyProvenance, bool) {
	repo, ref, found := strings.Cut(s, ":")
	if !found || ref == "" {
		return CopyProvenance{}, false
	}
	return CopyProvenance{RepoID: repo, SnapshotRef: ref}, true
}

// CopyProvenanceFromMeta reads the authoritative provenance off a snapshot's
// metadata. It reports false when the snapshot was not produced by `copy`.
func CopyProvenanceFromMeta(meta map[string]string) (CopyProvenance, bool) {
	ref := meta[MetaKeyCopyFromSnapshot]
	if ref == "" {
		return CopyProvenance{}, false
	}
	return CopyProvenance{RepoID: meta[MetaKeyCopyFromRepo], SnapshotRef: ref}, true
}

// Matches reports whether p and other name the same source snapshot.
//
// The snapshot ref carries the match; the repository id can only disqualify it.
// Two non-empty, unequal ids mean two different repositories and so never
// match, but an empty id on either side means "not recorded" — which is a
// routine state, not a mismatch:
//
//   - the source repository predates RepoConfig.ID (see there), or
//   - an older build rewrote the source marker and dropped the id, which it
//     does whenever it upgrades the repository format.
//
// Treating unknown as a distinct value would make the second case re-import an
// entire history the moment any older build touched the source. Matching on the
// ref alone is sound for an encrypted source, whose snapshot refs are
// content-addressed under its master key: a collision across two repositories
// implies a shared master key and identical content, at which point the two
// snapshots really are the same snapshot.
func (p CopyProvenance) Matches(other CopyProvenance) bool {
	if p.IsZero() || other.IsZero() {
		return false
	}
	if p.SnapshotRef != other.SnapshotRef {
		return false
	}
	return p.RepoID == "" || other.RepoID == "" || p.RepoID == other.RepoID
}

// ApplyTo returns meta with p recorded on it, allocating a map if needed. The
// input map is not modified, so a caller may pass a source snapshot's own Meta
// while copying it.
func (p CopyProvenance) ApplyTo(meta map[string]string) map[string]string {
	out := make(map[string]string, len(meta)+2)
	maps.Copy(out, meta)
	if p.IsZero() {
		return out
	}
	out[MetaKeyCopyFromSnapshot] = p.SnapshotRef
	if p.RepoID != "" {
		out[MetaKeyCopyFromRepo] = p.RepoID
	} else {
		// An empty RepoID is recorded by omission, not as an empty string, so
		// that a snapshot copied from a legacy source and one copied from a
		// source whose id happens to be "" cannot be told apart on read.
		delete(out, MetaKeyCopyFromRepo)
	}
	return out
}
