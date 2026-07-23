package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

var snapLog = logger.New("snapshots", logger.ColorCyan)

const snapshotCatalogKey = "index/snapshots"

// ---------------------------------------------------------------------------
// Snapshot catalog (index/snapshots)
// ---------------------------------------------------------------------------

// LoadSnapshotCatalog returns all snapshots, using the catalog index when
// available and falling back to individual GETs only for snapshots that are
// missing from the catalog. The catalog is automatically rebuilt/updated
// whenever a mismatch with the live snapshot keys is detected.
// Results are sorted newest-first by Created time.
func LoadSnapshotCatalog(s store.ObjectStore) ([]SnapshotEntry, error) {
	ctx := context.Background()

	// 1. Load catalog (best-effort).
	var catalog []core.SnapshotSummary
	if data, err := s.Get(ctx, snapshotCatalogKey); err == nil {
		_ = json.Unmarshal(data, &catalog)
	}

	// 2. List live snapshot keys for reconciliation.
	liveKeys, err := s.List(ctx, "snapshot/")
	if err != nil {
		return nil, err
	}

	liveSet := make(map[string]struct{}, len(liveKeys))
	for _, k := range liveKeys {
		liveSet[k] = struct{}{}
	}

	// 3. Index catalog by ref.
	catalogMap := make(map[string]core.SnapshotSummary, len(catalog))
	for _, s := range catalog {
		catalogMap[s.Ref] = s
	}

	// 4. Reconcile.
	needRebuild := false

	// Find refs in live but not in catalog → need to fetch.
	var missing []string
	for _, k := range liveKeys {
		if _, ok := catalogMap[k]; !ok {
			missing = append(missing, k)
			needRebuild = true
		}
	}

	// Find refs in catalog but not in live → stale.
	for _, cs := range catalog {
		if _, ok := liveSet[cs.Ref]; !ok {
			needRebuild = true
			break
		}
	}

	// 5. Fetch missing snapshot objects concurrently.
	if len(missing) > 0 {
		fetched, err := fetchSnapshots(s, missing)
		if err != nil {
			return nil, fmt.Errorf("load snapshot catalog: %w", err)
		}
		for ref, snap := range fetched {
			catalogMap[ref] = snapshotToSummary(ref, snap)
		}
	}

	// 6. Build result from live keys only (drops stale).
	entries := make([]SnapshotEntry, 0, len(liveKeys))
	var updatedCatalog []core.SnapshotSummary
	if needRebuild {
		updatedCatalog = make([]core.SnapshotSummary, 0, len(liveKeys))
	}

	for _, k := range liveKeys {
		cs, ok := catalogMap[k]
		if !ok {
			continue // could not fetch; skip
		}
		created, _ := time.Parse(time.RFC3339, cs.Created)
		entries = append(entries, SnapshotEntry{
			Ref: cs.Ref,
			Snap: core.Snapshot{
				Version:     1,
				Created:     cs.Created,
				Root:        cs.Root,
				Seq:         cs.Seq,
				Source:      cs.Source,
				Tags:        cs.Tags,
				ChangeToken: cs.ChangeToken,
				ExcludeHash: cs.ExcludeHash,
			},
			Created: created,
		})
		if needRebuild {
			updatedCatalog = append(updatedCatalog, cs)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Created.After(entries[j].Created)
	})

	// 7. Persist rebuilt catalog (best-effort).
	if needRebuild {
		if err := SaveSnapshotCatalog(s, updatedCatalog); err != nil {
			snapLog.Debugf("failed to persist snapshot catalog: %v", err)
		}
	}

	return entries, nil
}

// SaveSnapshotCatalog persists the full catalog to the store.
func SaveSnapshotCatalog(s store.ObjectStore, catalog []core.SnapshotSummary) error {
	data, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	return s.Put(context.Background(), snapshotCatalogKey, data)
}

// loadCatalogForUpdate reads the current catalog for AppendSnapshotCatalog and
// RemoveFromSnapshotCatalog. ok is false when the read failed for a real
// reason (not simply "no catalog yet"): callers must not treat that as an
// empty catalog, since persisting one would clobber every existing entry with
// a base that just happens to be wrong rather than absent. The self-heal in
// LoadSnapshotCatalog recovers a merely stale catalog on the next read; it
// cannot recover one that was overwritten with fabricated emptiness.
func loadCatalogForUpdate(s store.ObjectStore) (catalog []core.SnapshotSummary, ok bool) {
	data, err := s.Get(context.Background(), snapshotCatalogKey)
	switch {
	case err == nil:
		_ = json.Unmarshal(data, &catalog)
		return catalog, true
	case errors.Is(err, store.ErrNotFound):
		return nil, true
	default:
		return nil, false
	}
}

// AppendSnapshotCatalog loads the current catalog, appends a new summary, and
// persists it. This is best-effort; errors are logged but not propagated.
func AppendSnapshotCatalog(s store.ObjectStore, summary core.SnapshotSummary) {
	catalog, ok := loadCatalogForUpdate(s)
	if !ok {
		snapLog.Debugf("failed to append snapshot catalog: could not read existing catalog")
		return
	}
	catalog = append(catalog, summary)
	if err := SaveSnapshotCatalog(s, catalog); err != nil {
		snapLog.Debugf("failed to append snapshot catalog: %v", err)
	}
}

// RemoveFromSnapshotCatalog loads the current catalog, removes entries whose
// refs match, and persists the result. This is best-effort.
func RemoveFromSnapshotCatalog(s store.ObjectStore, refs ...string) {
	catalog, ok := loadCatalogForUpdate(s)
	if !ok {
		snapLog.Debugf("failed to update snapshot catalog after removal: could not read existing catalog")
		return
	}
	if len(catalog) == 0 {
		return
	}
	remove := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		remove[r] = struct{}{}
	}
	filtered := make([]core.SnapshotSummary, 0, len(catalog))
	for _, cs := range catalog {
		if _, ok := remove[cs.Ref]; !ok {
			filtered = append(filtered, cs)
		}
	}
	if err := SaveSnapshotCatalog(s, filtered); err != nil {
		snapLog.Debugf("failed to update snapshot catalog after removal: %v", err)
	}
}

// snapshotToSummary converts a full Snapshot and its ref into a SnapshotSummary.
func snapshotToSummary(ref string, snap core.Snapshot) core.SnapshotSummary {
	return core.SnapshotSummary{
		Ref:         ref,
		Seq:         snap.Seq,
		Created:     snap.Created,
		Root:        snap.Root,
		Source:      snap.Source,
		Tags:        snap.Tags,
		ChangeToken: snap.ChangeToken,
		ExcludeHash: snap.ExcludeHash,
	}
}

// ---------------------------------------------------------------------------
// Slow path (individual GETs)
// ---------------------------------------------------------------------------

// fetchSnapshots concurrently fetches and unmarshals the given snapshot keys.
// A key that no longer exists (e.g. removed by a concurrent prune) is simply
// omitted from the result — that is a legitimate "gone". Any other failure,
// including a snapshot that fails to decode, aborts the whole fetch instead:
// the caller cannot tell "gone" from "unreadable", and silently dropping an
// unreadable-but-still-live snapshot would persist a catalog that is missing
// an entry that is actually still there.
func fetchSnapshots(s store.ObjectStore, keys []string) (map[string]core.Snapshot, error) {
	ctx := context.Background()
	result := make(map[string]core.Snapshot, len(keys))
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup

	for _, key := range keys {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			data, err := s.Get(ctx, k)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("fetch %s: %w", k, err)
				}
				mu.Unlock()
				return
			}
			var snap core.Snapshot
			if err := json.Unmarshal(data, &snap); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("decode %s: %w", k, err)
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			result[k] = snap
			mu.Unlock()
		}(key)
	}

	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// index/latest helpers
// ---------------------------------------------------------------------------

// resolveLatest reads index/latest and returns the snapshot ref it points to.
// Returns ("", 0, nil) on a fresh repository (no index/latest yet). Any other
// read or decode failure is returned as an error rather than treated as a
// fresh repo — a transient store error is not the same as "no snapshots
// exist", and confusing the two would silently reset the sequence number and
// downgrade the next backup from incremental to a full rescan.
func resolveLatest(s store.ObjectStore) (ref string, seq int, err error) {
	data, err := s.Get(context.Background(), "index/latest")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", 0, nil
		}
		return "", 0, fmt.Errorf("read index/latest: %w", err)
	}
	var idx core.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return "", 0, fmt.Errorf("decode index/latest: %w", err)
	}
	return idx.LatestSnapshot, idx.Seq, nil
}

// updateLatest sets index/latest to point to the given snapshot, or deletes it
// if ref is empty.
func updateLatest(s store.ObjectStore, ref string, seq int) error {
	ctx := context.Background()
	if ref == "" {
		return s.Delete(ctx, "index/latest")
	}
	data, _ := json.Marshal(core.Index{LatestSnapshot: ref, Seq: seq})
	return s.Put(ctx, "index/latest", data)
}
