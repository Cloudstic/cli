package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

const fileMetaCatalogKey = "index/filemeta"

// LoadFileMetaCache returns a ref→FileMeta map that is always consistent with
// the store. It lists all live filemeta/ keys and loads the cached catalog,
// then reconciles:
//
//   - key exists + in cache   → trust the cached value (zero cost)
//   - key exists + not cached → fetch from store, add to cache
//   - key gone   + in cache   → drop from cache
//
// The updated cache is flushed back to the store when anything changed.
func LoadFileMetaCache(s store.ObjectStore) (map[string]core.FileMeta, error) {
	ctx := context.Background()

	keys, err := s.List(ctx, "filemeta/")
	if err != nil {
		return nil, err
	}

	liveSet := make(map[string]bool, len(keys))
	for _, k := range keys {
		liveSet[k] = true
	}

	catalog := loadFileMetaCatalog(s)

	hits, fetched, stale := 0, 0, 0

	// Drop stale entries (in cache but key no longer in store).
	for ref := range catalog {
		if !liveSet[ref] {
			debugf("stale  %s", ref)
			delete(catalog, ref)
			stale++
		}
	}

	// Fetch missing entries (key exists but not cached).
	for _, key := range keys {
		if _, ok := catalog[key]; ok {
			hits++
			continue
		}
		debugf("fetch  %s", key)
		data, err := s.Get(ctx, key)
		if err != nil {
			continue
		}
		var fm core.FileMeta
		if err := json.Unmarshal(data, &fm); err != nil {
			continue
		}
		catalog[key] = fm
		fetched++
	}

	dirty := fetched > 0 || stale > 0
	debugf("filemeta cache: %s%d%s listed, %s%d%s hits, %s%d%s fetched, %s%d%s stale",
		dGreen, len(keys), dReset, dGreen, hits, dReset, dGreen, fetched, dReset, dGreen, stale, dReset)

	if DebugWriter != nil {
		for ref, fm := range catalog {
			debugf("  %s%-60s%s %s (%s%s%s)", dDim, ref, dReset, fm.Name, dDim, fm.Type, dReset)
		}
	}

	if dirty {
		_ = saveFileMetaCatalog(s, catalog)
	}

	return catalog, nil
}

// AddFileMetasToIndex merges a batch of file-meta entries into the catalog.
// It is safe to call even when the catalog does not yet exist.
func AddFileMetasToIndex(s store.ObjectStore, metas map[string]core.FileMeta) error {
	if len(metas) == 0 {
		return nil
	}
	catalog := loadFileMetaCatalog(s)
	for ref, fm := range metas {
		catalog[ref] = fm
	}
	return saveFileMetaCatalog(s, catalog)
}

// ---------------------------------------------------------------------------
// Catalog persistence helpers
// ---------------------------------------------------------------------------

func loadFileMetaCatalog(s store.ObjectStore) core.FileMetaCatalog {
	data, err := s.Get(context.Background(), fileMetaCatalogKey)
	if err != nil {
		return core.FileMetaCatalog{}
	}
	var cat core.FileMetaCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return core.FileMetaCatalog{}
	}
	return cat
}

func saveFileMetaCatalog(s store.ObjectStore, catalog core.FileMetaCatalog) error {
	data, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	return s.Put(context.Background(), fileMetaCatalogKey, data)
}

const (
	dDim    = "\033[2m"
	dCyan   = "\033[36m"
	dGreen  = "\033[32m"
	dYellow = "\033[33m"
	dReset  = "\033[0m"
)

func debugf(format string, args ...any) {
	if DebugWriter != nil {
		fmt.Fprintf(DebugWriter, dCyan+"[index]"+dReset+" "+format+"\n", args...)
	}
}
