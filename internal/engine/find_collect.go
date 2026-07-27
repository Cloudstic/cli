package engine

import (
	"sort"
	"strings"

	"github.com/cloudstic/cli/internal/core"
)

// findObservation is one sighting: this exact metadata object, under these
// paths, in this snapshot. Both scanners emit observations and nothing else,
// which is what lets the delta scan and the straightforward walk share an
// aggregation step — and therefore be compared for equality.
type findObservation struct {
	fileID   string
	ref      string
	meta     core.FileMeta
	paths    []string
	snapshot SnapshotRef
	source   *core.SourceInfo
}

// findCollector collapses observations into the result model.
//
// A repository duplicates a file along four independent axes, and each is
// handled differently:
//
//   - The same file unchanged across many snapshots shares one filemeta ref, so
//     it collapses into one version whose Snapshots list has many entries. The
//     refs are literally equal; treating them as one row is the accurate reading
//     of what the repository holds, not a display convenience.
//   - The same file edited yields a new ref per edit, so it becomes several
//     versions under one match — which is the view someone recovering a file
//     actually wants.
//   - Two distinct files with identical bytes have different refs and stay
//     separate matches. Collapsing them would be wrong: restoring one is not
//     restoring the other. GroupByContent regroups them on request.
//   - One file reachable by several paths keeps them all on the version.
type findCollector struct {
	groupByContent bool
	max            int
	truncated      bool

	order    []string
	groups   map[string]*findGroup
	versions map[string]map[string]*findVersionAccum
}

type findGroup struct {
	fileID      string
	contentHash string
	source      *core.SourceInfo
	ftype       core.FileType
}

type findVersionAccum struct {
	version   FileVersion
	snapshots map[string]SnapshotRef
}

func newFindCollector(groupByContent bool, max int) *findCollector {
	return &findCollector{
		groupByContent: groupByContent,
		max:            max,
		groups:         make(map[string]*findGroup),
		versions:       make(map[string]map[string]*findVersionAccum),
	}
}

func (c *findCollector) observe(o findObservation) {
	groupKey := o.fileID
	if c.groupByContent {
		groupKey = o.meta.ContentHash
	}

	if _, known := c.groups[groupKey]; !known {
		// The cap bounds distinct files, not versions: a match already being
		// reported keeps accumulating its history, so a truncated result never
		// shows a file with part of its versions missing.
		if len(c.groups) >= c.max {
			c.truncated = true
			return
		}
		group := &findGroup{contentHash: o.meta.ContentHash, source: o.source, ftype: metaFileType(&o.meta)}
		if !c.groupByContent {
			group.fileID = o.fileID
		}
		c.groups[groupKey] = group
		c.versions[groupKey] = make(map[string]*findVersionAccum)
		c.order = append(c.order, groupKey)
	}

	versionKey := findVersionKey(o)
	accum, ok := c.versions[groupKey][versionKey]
	if !ok {
		accum = &findVersionAccum{
			version: FileVersion{
				Ref:         o.ref,
				FileID:      o.fileID,
				Name:        o.meta.Name,
				Paths:       append([]string(nil), o.paths...),
				ContentHash: o.meta.ContentHash,
				Type:        metaFileType(&o.meta),
				Size:        o.meta.Size,
				Mtime:       o.meta.Mtime,
				Mode:        o.meta.Mode,
			},
			snapshots: make(map[string]SnapshotRef),
		}
		c.versions[groupKey][versionKey] = accum
	}
	accum.snapshots[o.snapshot.Ref] = o.snapshot
}

// findVersionKey identifies one version within its group.
//
// The ref alone is not enough. Renaming an ancestor folder changes a file's path
// without changing the file's own metadata object, so one ref can legitimately
// sit at different paths in different snapshots — and reporting only one of them
// would silently pick a branch.
func findVersionKey(o findObservation) string {
	var b strings.Builder
	b.WriteString(o.ref)
	b.WriteByte(0)
	b.WriteString(o.fileID)
	for _, p := range o.paths {
		b.WriteByte(0)
		b.WriteString(p)
	}
	return b.String()
}

// finish assembles the matches: versions newest-first within each match, matches
// ordered by path so repeated runs of the same query agree.
func (c *findCollector) finish() []FileMatch {
	matches := make([]FileMatch, 0, len(c.order))
	for _, groupKey := range c.order {
		group := c.groups[groupKey]

		versions := make([]FileVersion, 0, len(c.versions[groupKey]))
		for _, accum := range c.versions[groupKey] {
			v := accum.version
			v.Snapshots = sortedSnapshotRefs(accum.snapshots)
			if len(v.Snapshots) > 0 {
				v.FirstSeen = v.Snapshots[len(v.Snapshots)-1].Created
				v.LastSeen = v.Snapshots[0].Created
			}
			versions = append(versions, v)
		}
		sortFileVersions(versions)

		matches = append(matches, FileMatch{
			FileID:      group.fileID,
			ContentHash: group.contentHash,
			Source:      group.source,
			Type:        group.ftype,
			Versions:    versions,
		})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if pi, pj := matches[i].Path(), matches[j].Path(); pi != pj {
			return pi < pj
		}
		return matches[i].FileID < matches[j].FileID
	})
	return matches
}

// sortedSnapshotRefs returns the snapshots newest-first.
func sortedSnapshotRefs(set map[string]SnapshotRef) []SnapshotRef {
	refs := make([]SnapshotRef, 0, len(set))
	for _, r := range set {
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Created != refs[j].Created {
			return refs[i].Created > refs[j].Created
		}
		if refs[i].Seq != refs[j].Seq {
			return refs[i].Seq > refs[j].Seq
		}
		return refs[i].Ref < refs[j].Ref
	})
	return refs
}

// sortFileVersions orders versions newest-first by the latest snapshot holding
// them, so "which version do I want?" reads top-down.
func sortFileVersions(versions []FileVersion) {
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].LastSeen != versions[j].LastSeen {
			return versions[i].LastSeen > versions[j].LastSeen
		}
		// LastSeen is an ISO8601 timestamp truncated to the second, so two
		// snapshots taken within the same second — routine in a fast backup
		// loop or a test — compare equal here. Snapshots[0].Seq (the version's
		// newest holding snapshot; sortedSnapshotRefs already put it first) is
		// a monotonic counter with no such resolution limit, and is what
		// actually decides which version is newer in that case. Falling
		// through to Mtime below without this would let two versions edited
		// in the same second get ordered by their content hash instead —
		// deterministic, but unrelated to recency.
		if si, sj := lastSnapshotSeq(versions[i]), lastSnapshotSeq(versions[j]); si != sj {
			return si > sj
		}
		if versions[i].Mtime != versions[j].Mtime {
			return versions[i].Mtime > versions[j].Mtime
		}
		if len(versions[i].Paths) > 0 && len(versions[j].Paths) > 0 && versions[i].Paths[0] != versions[j].Paths[0] {
			return versions[i].Paths[0] < versions[j].Paths[0]
		}
		return versions[i].Ref < versions[j].Ref
	})
}

// lastSnapshotSeq returns the sequence number of the newest snapshot holding
// v, or 0 if v is (unexpectedly) held by none.
func lastSnapshotSeq(v FileVersion) int {
	if len(v.Snapshots) == 0 {
		return 0
	}
	return v.Snapshots[0].Seq
}
