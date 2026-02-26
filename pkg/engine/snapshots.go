package engine

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/store"
)

const snapshotCatalogKey = "index/snapshots"

// ListAllSnapshots enumerates every snapshot in the store. It reconciles the
// index/snapshots catalog against the live snapshot/ key listing so the catalog
// is self-healing: missing entries are fetched from the store and stale entries
// are pruned. Results are sorted newest-first by Created time.
func ListAllSnapshots(s store.ObjectStore) ([]SnapshotEntry, error) {
	keys, err := s.List("snapshot/")
	if err != nil {
		return nil, err
	}

	liveSet := make(map[string]bool, len(keys))
	for _, k := range keys {
		liveSet[k] = true
	}

	catalog := loadCatalog(s)
	indexed := catalogToMap(catalog)
	dirty := false

	// Remove stale entries (in catalog but no longer on disk).
	for ref := range indexed {
		if !liveSet[ref] {
			delete(indexed, ref)
			dirty = true
		}
	}

	// Add missing entries (on disk but not in catalog).
	for _, key := range keys {
		if _, ok := indexed[key]; ok {
			continue
		}
		data, err := s.Get(key)
		if err != nil {
			continue
		}
		var snap core.Snapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			continue
		}
		indexed[key] = snapshotToSummary(snap, key)
		dirty = true
	}

	if dirty {
		saveCatalog(s, indexed)
	}

	entries := make([]SnapshotEntry, 0, len(indexed))
	for _, sum := range indexed {
		created, _ := time.Parse(time.RFC3339, sum.Created)
		entries = append(entries, SnapshotEntry{
			Ref:     sum.Ref,
			Snap:    summaryToSnapshot(sum),
			Created: created,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Created.After(entries[j].Created)
	})

	return entries, nil
}

// AddSnapshotToIndex appends a snapshot to the catalog. It is safe to call
// even when the catalog does not yet exist.
func AddSnapshotToIndex(s store.ObjectStore, snap core.Snapshot, ref string) error {
	catalog := loadCatalog(s)
	indexed := catalogToMap(catalog)
	indexed[ref] = snapshotToSummary(snap, ref)
	return saveCatalog(s, indexed)
}

// RemoveSnapshotFromIndex removes a snapshot from the catalog. It is safe to
// call even when the ref is not present.
func RemoveSnapshotFromIndex(s store.ObjectStore, ref string) error {
	catalog := loadCatalog(s)
	indexed := catalogToMap(catalog)
	if _, ok := indexed[ref]; !ok {
		return nil
	}
	delete(indexed, ref)
	return saveCatalog(s, indexed)
}

// ---------------------------------------------------------------------------
// index/latest helpers
// ---------------------------------------------------------------------------

// resolveLatest reads index/latest and returns the snapshot ref it points to.
// Returns empty string on error (fresh repo).
func resolveLatest(s store.ObjectStore) (ref string, seq int) {
	data, err := s.Get("index/latest")
	if err != nil {
		return "", 0
	}
	var idx core.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return "", 0
	}
	return idx.LatestSnapshot, idx.Seq
}

// updateLatest sets index/latest to point to the given snapshot, or deletes it
// if ref is empty.
func updateLatest(s store.ObjectStore, ref string, seq int) error {
	if ref == "" {
		return s.Delete("index/latest")
	}
	data, _ := json.Marshal(core.Index{LatestSnapshot: ref, Seq: seq})
	return s.Put("index/latest", data)
}

// ---------------------------------------------------------------------------
// Catalog persistence helpers
// ---------------------------------------------------------------------------

func loadCatalog(s store.ObjectStore) core.SnapshotCatalog {
	data, err := s.Get(snapshotCatalogKey)
	if err != nil {
		return core.SnapshotCatalog{}
	}
	var cat core.SnapshotCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return core.SnapshotCatalog{}
	}
	return cat
}

func catalogToMap(cat core.SnapshotCatalog) map[string]core.SnapshotSummary {
	m := make(map[string]core.SnapshotSummary, len(cat.Snapshots))
	for _, sum := range cat.Snapshots {
		m[sum.Ref] = sum
	}
	return m
}

func saveCatalog(s store.ObjectStore, indexed map[string]core.SnapshotSummary) error {
	summaries := make([]core.SnapshotSummary, 0, len(indexed))
	for _, sum := range indexed {
		summaries = append(summaries, sum)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Seq > summaries[j].Seq
	})
	data, err := json.Marshal(core.SnapshotCatalog{Snapshots: summaries})
	if err != nil {
		return err
	}
	return s.Put(snapshotCatalogKey, data)
}

func snapshotToSummary(snap core.Snapshot, ref string) core.SnapshotSummary {
	return core.SnapshotSummary{
		Ref:         ref,
		Seq:         snap.Seq,
		Created:     snap.Created,
		Root:        snap.Root,
		Source:      snap.Source,
		Tags:        snap.Tags,
		ChangeToken: snap.ChangeToken,
	}
}

// summaryToSnapshot reconstructs a Snapshot from a catalog summary. Fields not
// stored in the summary (Version, Meta) default to zero values.
func summaryToSnapshot(sum core.SnapshotSummary) core.Snapshot {
	return core.Snapshot{
		Seq:         sum.Seq,
		Created:     sum.Created,
		Root:        sum.Root,
		Source:      sum.Source,
		Tags:        sum.Tags,
		ChangeToken: sum.ChangeToken,
	}
}
