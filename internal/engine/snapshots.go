package engine

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

// ListAllSnapshots enumerates every snapshot in the store.
// Results are sorted newest-first by Created time.
func ListAllSnapshots(s store.ObjectStore) ([]SnapshotEntry, error) {
	ctx := context.Background()

	keys, err := s.List(ctx, "snapshot/")
	if err != nil {
		return nil, err
	}

	entries := make([]SnapshotEntry, 0, len(keys))

	// Ensure concurrent fetch of snapshots
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, key := range keys {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			data, err := s.Get(ctx, k)
			if err != nil {
				return
			}
			var snap core.Snapshot
			if err := json.Unmarshal(data, &snap); err != nil {
				return
			}
			created, _ := time.Parse(time.RFC3339, snap.Created)

			mu.Lock()
			entries = append(entries, SnapshotEntry{
				Ref:     k,
				Snap:    snap,
				Created: created,
			})
			mu.Unlock()
		}(key)
	}

	wg.Wait()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Created.After(entries[j].Created)
	})

	return entries, nil
}

// ---------------------------------------------------------------------------
// index/latest helpers
// ---------------------------------------------------------------------------

// resolveLatest reads index/latest and returns the snapshot ref it points to.
// Returns empty string on error (fresh repo).
func resolveLatest(s store.ObjectStore) (ref string, seq int) {
	data, err := s.Get(context.Background(), "index/latest")
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
	ctx := context.Background()
	if ref == "" {
		return s.Delete(ctx, "index/latest")
	}
	data, _ := json.Marshal(core.Index{LatestSnapshot: ref, Seq: seq})
	return s.Put(ctx, "index/latest", data)
}
