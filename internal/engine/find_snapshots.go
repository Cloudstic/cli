package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudstic/cli/pkg/store"
)

// findLineage is a run of snapshots that share a source identity, ordered
// oldest-first.
//
// Grouping matters because the delta scan diffs consecutive roots: diffing two
// snapshots of unrelated sources is meaningless and costs two full walks, since
// nothing structural is shared between them. The grouping is the one forget
// policies already use (RFC 0009), so the vocabulary is shared rather than
// reinvented.
type findLineage struct {
	key       GroupKey
	snapshots []SnapshotEntry
}

// groupFindLineages partitions selected snapshots by source identity and orders
// each group oldest-first, which is the order the delta scan walks.
func groupFindLineages(entries []SnapshotEntry) []findLineage {
	groups := groupSnapshots(entries, defaultGroupFields())

	keys := make([]GroupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	lineages := make([]findLineage, 0, len(keys))
	for _, k := range keys {
		group := groups[k]
		sort.Slice(group, func(i, j int) bool {
			if group[i].Snap.Seq != group[j].Snap.Seq {
				return group[i].Snap.Seq < group[j].Snap.Seq
			}
			return group[i].Created.Before(group[j].Created)
		})
		lineages = append(lineages, findLineage{key: k, snapshots: group})
	}
	return lineages
}

// selectFindSnapshots narrows the catalog to the snapshots a query searches.
//
// The default is every snapshot, which is the right default only because the
// delta scan makes it affordable: the file a user is hunting for is usually one
// that is no longer in the latest snapshot.
func selectFindSnapshots(s store.ObjectStore, catalog []SnapshotEntry, q FindQuery) ([]SnapshotEntry, error) {
	selected := catalog

	if len(q.Snapshots) > 0 {
		var err error
		if selected, err = resolveFindSnapshotRefs(s, catalog, q.Snapshots); err != nil {
			return nil, err
		}
	}

	if q.Source != "" || len(q.Tags) > 0 {
		filter := snapshotFilter{tags: q.Tags}
		// A source selector may name a bare type ("local") or a full URI
		// ("local:./Documents"); both forms are already understood by the
		// filter used by list and forget.
		if q.Source != "" {
			scheme, path, _ := strings.Cut(q.Source, ":")
			filter.source = scheme
			filter.path = strings.TrimPrefix(path, "//")
		}
		var kept []SnapshotEntry
		for _, e := range selected {
			if matchesFilter(&e.Snap, filter) {
				kept = append(kept, e)
			}
		}
		selected = kept
	}

	if q.Since != "" || q.Until != "" {
		since, until, err := parseSnapshotWindow(q)
		if err != nil {
			return nil, err
		}
		var kept []SnapshotEntry
		for _, e := range selected {
			if since != nil && e.Created.Before(*since) {
				continue
			}
			if until != nil && e.Created.After(*until) {
				continue
			}
			kept = append(kept, e)
		}
		selected = kept
	}

	if q.Latest > 0 {
		// The catalog is newest-first, so take the head, then restore that
		// order for the caller.
		newestFirst := make([]SnapshotEntry, len(selected))
		copy(newestFirst, selected)
		sort.Slice(newestFirst, func(i, j int) bool { return newestFirst[i].Created.After(newestFirst[j].Created) })
		if len(newestFirst) > q.Latest {
			newestFirst = newestFirst[:q.Latest]
		}
		selected = newestFirst
	}

	return selected, nil
}

func parseSnapshotWindow(q FindQuery) (since, until *time.Time, err error) {
	if q.Since != "" {
		t, perr := ParseFindTime(q.Since)
		if perr != nil {
			return nil, nil, fmt.Errorf("-since: %w", perr)
		}
		since = &t
	}
	if q.Until != "" {
		t, perr := ParseFindTime(q.Until)
		if perr != nil {
			return nil, nil, fmt.Errorf("-until: %w", perr)
		}
		until = &t
	}
	return since, until, nil
}

// resolveFindSnapshotRefs maps each selector onto a catalog entry. Selectors may
// be a full "snapshot/<hash>" ref, a bare hash, an unambiguous hash prefix, or
// "latest".
func resolveFindSnapshotRefs(s store.ObjectStore, catalog []SnapshotEntry, selectors []string) ([]SnapshotEntry, error) {
	byRef := make(map[string]SnapshotEntry, len(catalog))
	for _, e := range catalog {
		byRef[e.Ref] = e
	}

	seen := make(map[string]bool, len(selectors))
	var out []SnapshotEntry
	for _, sel := range selectors {
		entry, err := resolveOneFindSnapshot(s, catalog, byRef, sel)
		if err != nil {
			return nil, err
		}
		if seen[entry.Ref] {
			continue
		}
		seen[entry.Ref] = true
		out = append(out, entry)
	}
	return out, nil
}

func resolveOneFindSnapshot(
	s store.ObjectStore,
	catalog []SnapshotEntry,
	byRef map[string]SnapshotEntry,
	selector string,
) (SnapshotEntry, error) {
	if selector == "latest" {
		ref, _, err := resolveLatest(s)
		if err != nil {
			return SnapshotEntry{}, err
		}
		if entry, ok := byRef[ref]; ok {
			return entry, nil
		}
		return SnapshotEntry{}, snapshotNotFoundError(selector)
	}

	ref := selector
	if !strings.HasPrefix(ref, "snapshot/") {
		ref = "snapshot/" + ref
	}
	if entry, ok := byRef[ref]; ok {
		return entry, nil
	}

	var matches []SnapshotEntry
	for _, e := range catalog {
		if strings.HasPrefix(e.Ref, ref) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return SnapshotEntry{}, snapshotNotFoundError(selector)
	default:
		return SnapshotEntry{}, snapshotRefAmbiguousError(selector, len(matches))
	}
}
