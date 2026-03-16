package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudstic/cli/internal/core"
	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	maxPackSize   = 8 * 1024 * 1024 // 8 MB
	maxObjectSize = 512 * 1024      // Only bundle objects smaller than 512 KB
	packPrefix    = "packs/"
	indexPacksKey = "index/packs"
)

// PackStore wraps an ObjectStore to aggregate small objects into larger "packfiles".
// It uses a stateless JSON catalog ("index/packs") to keep track of which pack
// contains which object.
type PackStore struct {
	ObjectStore

	mu sync.RWMutex

	// Active bundle for small object writes
	packBuffer *bytes.Buffer
	packKeys   map[string]PackEntry

	// The stateless JSON catalog mapped from index/packs
	catalog       map[string]PackEntry
	catalogDirty  bool
	catalogLoaded bool

	// LRU cache for recently downloaded packfiles to accelerate Get() and HAMT walks
	packCache *lru.Cache[string, []byte]
}

// PackEntry represents the location of a small object within a packfile.
type PackEntry struct {
	PackRef string `json:"p"`
	Offset  int64  `json:"o"`
	Length  int64  `json:"l"`
}

// NewPackStore initializes a new MicroPackStore over an existing ObjectStore.
func NewPackStore(inner ObjectStore) (*PackStore, error) {
	debugf("init packstore: LRU size=%d", 4)
	// Keep up to 30 MB of packfiles in memory (around 4 packs) to speed up reads
	cache, err := lru.New[string, []byte](4)
	if err != nil {
		return nil, fmt.Errorf("pack cache init: %w", err)
	}

	return &PackStore{
		ObjectStore: inner,
		packBuffer:  new(bytes.Buffer),
		packKeys:    make(map[string]PackEntry),
		catalog:     make(map[string]PackEntry),
		packCache:   cache,
	}, nil
}

// Put stores data either in the active packbuffer or directly to the inner store.
func (s *PackStore) Put(ctx context.Context, key string, data []byte) error {
	// 1. Bypass large objects (e.g. content blocks) and index/packs itself
	if !s.isSmallObject(key, data) {
		return s.ObjectStore.Put(ctx, key, data)
	}

	debugf("pack: buffering %s (%d bytes)", key, len(data))

	s.mu.Lock()

	// 2. Append to active buffer
	offset := int64(s.packBuffer.Len())
	s.packBuffer.Write(data)

	// 3. Record its location
	s.packKeys[key] = PackEntry{
		Offset: offset,
		Length: int64(len(data)),
		// PackRef is set when the pack is flushed
	}

	var packRef string
	var packData []byte

	// 4. If buffer is full, prepare it for flushing
	if s.packBuffer.Len() >= maxPackSize {
		packRef, packData = s.prepareFlushLocked()
	}

	s.mu.Unlock()

	// 5. Upload outside the lock so we don't block concurrent operations
	if packRef != "" {
		if err := s.ObjectStore.Put(ctx, packRef, packData); err != nil {
			return fmt.Errorf("flush pack %s: %w", packRef, err)
		}
	}

	return nil
}

// isSmallObject determines if a key/data pair should be bundled into a packfile.
func (s *PackStore) isSmallObject(key string, data []byte) bool {
	// Don't pack the pack index itself, lock files, or the snapshot catalog
	// (mutable indexes that are read on every operation).
	if key == indexPacksKey || key == "index/snapshots" || strings.HasPrefix(key, "index/lock") {
		return false
	}
	// We only pack metadata to keep data files randomly accessible natively.
	// Specifically: filemeta, nodes, snapshots, index, and small chunks/contents (up to 512KB)
	if len(data) <= maxObjectSize {
		if strings.HasPrefix(key, "filemeta/") ||
			strings.HasPrefix(key, "node/") ||
			strings.HasPrefix(key, "snapshot/") ||
			strings.HasPrefix(key, "chunk/") ||
			strings.HasPrefix(key, "content/") ||
			strings.HasPrefix(key, "index/") {
			return true
		}
	}
	return false
}

// prepareFlushLocked takes the current buffer, prepares a pack object, updates the catalog,
// and returns the data to be uploaded so it can be done without holding the mutex.
// mu must be locked before calling.
func (s *PackStore) prepareFlushLocked() (string, []byte) {
	if s.packBuffer.Len() == 0 {
		return "", nil
	}

	// Hash the buffer contents to create a reproducible packfile name
	packHash := core.ComputeHash(s.packBuffer.Bytes())
	packRef := packPrefix + packHash
	debugf("preparing packfile %s with %d objects (%d bytes)", packRef, len(s.packKeys), s.packBuffer.Len())

	// Copy the buffer since it will be uploaded asynchronously
	packData := make([]byte, s.packBuffer.Len())
	copy(packData, s.packBuffer.Bytes())

	// Cache it immediately since we just wrote it, will speed up following Reads
	s.packCache.Add(packRef, packData)

	// Assign the PackRef to all entries that were bundled and move to catalog
	for key, entry := range s.packKeys {
		entry.PackRef = packRef
		s.catalog[key] = entry
	}
	s.catalogDirty = true

	// Reset active buffer
	s.packBuffer.Reset()
	s.packKeys = make(map[string]PackEntry)

	return packRef, packData
}

// Get retrieves an object from the active buffer, a cached pack, or downloads the pack.
func (s *PackStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.RLock()

	// 1. Is it currently in the active memory buffer?
	if entry, ok := s.packKeys[key]; ok {
		data := make([]byte, entry.Length)
		copy(data, s.packBuffer.Bytes()[entry.Offset:entry.Offset+entry.Length])
		s.mu.RUnlock()
		debugf("get %s: hit active buffer (len=%d)", key, entry.Length)
		return data, nil
	}

	// 2. Is it in our catalog?
	entry, inCatalog := s.catalog[key]
	s.mu.RUnlock()

	// If not in catalog, wait: we might need to load the catalog from the remote store first,
	// or it's just a normal large object.
	if !inCatalog {
		if key != indexPacksKey && !strings.HasPrefix(key, packPrefix) {
			s.mu.Lock()
			// Another thread might have loaded it while we waited for lock
			if !s.catalogLoaded {
				_ = s.loadCatalogLocked(ctx)
			}
			entry, inCatalog = s.catalog[key]
			s.mu.Unlock()
		}

		if !inCatalog {
			// It's a large object or an object not in a packfile; get it directly.
			return s.ObjectStore.Get(ctx, key)
		}
	}

	// 3. We know it's in a pack. Do we have the pack in the LRU cache?
	if packData, ok := s.packCache.Get(entry.PackRef); ok {
		if int64(len(packData)) < entry.Offset+entry.Length {
			return nil, fmt.Errorf("packfile %s is smaller than expected for key %s", entry.PackRef, key)
		}
		data := make([]byte, entry.Length)
		copy(data, packData[entry.Offset:entry.Offset+entry.Length])
		debugf("get %s: hit lru pack cache %s (len=%d)", key, entry.PackRef, entry.Length)
		return data, nil
	}

	debugf("get %s: downloading pack %s", key, entry.PackRef)
	// 4. Download the entire packfile, cache it, and return the slice
	packData, err := s.ObjectStore.Get(ctx, entry.PackRef)
	if err != nil {
		return nil, fmt.Errorf("fetch pack %s for key %s: %w", entry.PackRef, key, err)
	}

	s.packCache.Add(entry.PackRef, packData)

	if int64(len(packData)) < entry.Offset+entry.Length {
		return nil, fmt.Errorf("downloaded packfile %s is too small", entry.PackRef)
	}

	data := make([]byte, entry.Length)
	copy(data, packData[entry.Offset:entry.Offset+entry.Length])
	return data, nil
}

// Exists checks the un-flushed buffer, the catalog, or falls back to inner.
func (s *PackStore) Exists(ctx context.Context, key string) (bool, error) {
	s.mu.RLock()
	if _, ok := s.packKeys[key]; ok {
		s.mu.RUnlock()
		return true, nil
	}
	if _, ok := s.catalog[key]; ok {
		s.mu.RUnlock()
		return true, nil
	}
	s.mu.RUnlock()

	return s.ObjectStore.Exists(ctx, key)
}

// List returns all keys matching the prefix, merging results from the inner store
// with the keys currently buffered or indexed in packfiles.
func (s *PackStore) List(ctx context.Context, prefix string) ([]string, error) {
	// 1. Get from inner store
	innerKeys, err := s.ObjectStore.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	// 2. Add keys from catalog and active buffer matching prefix
	s.mu.RLock()
	var packedKeys []string
	if !s.catalogLoaded {
		// Try to load catalog if not loaded
		s.mu.RUnlock()
		s.mu.Lock()
		if !s.catalogLoaded {
			_ = s.loadCatalogLocked(ctx)
		}
		s.mu.Unlock()
		s.mu.RLock()
	}

	for key := range s.catalog {
		if strings.HasPrefix(key, prefix) {
			packedKeys = append(packedKeys, key)
		}
	}
	for key := range s.packKeys {
		if strings.HasPrefix(key, prefix) {
			packedKeys = append(packedKeys, key)
		}
	}
	s.mu.RUnlock()

	// 3. Merge and deduplicate, ignoring raw packfiles
	keySet := make(map[string]struct{}, len(innerKeys)+len(packedKeys))
	for _, k := range innerKeys {
		if strings.HasPrefix(k, packPrefix) || k == indexPacksKey {
			continue
		}
		keySet[k] = struct{}{}
	}
	for _, k := range packedKeys {
		keySet[k] = struct{}{}
	}

	result := make([]string, 0, len(keySet))
	for k := range keySet {
		result = append(result, k)
	}
	return result, nil
}

// Flush ensures any pending small objects are written to a packfile,
// and uploads the latest JSON catalog.
func (s *PackStore) Flush(ctx context.Context) error {
	s.mu.Lock()
	packRef, packData := s.prepareFlushLocked()

	var catalogBytes []byte
	var err error
	if s.catalogDirty {
		catalogBytes, err = json.Marshal(s.catalog)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("marshal catalog: %w", err)
		}
	}
	s.mu.Unlock()

	if packRef != "" {
		if err := s.ObjectStore.Put(ctx, packRef, packData); err != nil {
			return fmt.Errorf("flush pack %s: %w", packRef, err)
		}
	}

	if catalogBytes != nil {
		if err := s.ObjectStore.Put(ctx, indexPacksKey, catalogBytes); err != nil {
			return fmt.Errorf("upload catalog: %w", err)
		}
		s.mu.Lock()
		s.catalogDirty = false
		totalEntries := len(s.catalog)
		nodeCount := 0
		for k := range s.catalog {
			if strings.HasPrefix(k, "node/") {
				nodeCount++
			}
		}
		s.mu.Unlock()
		debugf("pack: catalog flushed — %d total entries, %d node/* entries", totalEntries, nodeCount)
	}

	return nil
}

// loadCatalogLocked fetches the index/packs file from the inner store and populates the catalog.
func (s *PackStore) loadCatalogLocked(ctx context.Context) error {
	// If it was already loaded or proven missing (noted by dirty flag or some other state), we wouldn't be here,
	// but we need to track if we've already attempted loading so we don't spam 404s.
	if s.catalogLoaded {
		return nil
	}

	debugf("loading pack catalog from %s", indexPacksKey)
	data, err := s.ObjectStore.Get(ctx, indexPacksKey)
	if err != nil {
		// It's normal for index/packs to not exist on a fresh repository
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "NoSuchKey") {
			s.catalogLoaded = true // We checked, it doesn't exist, stop checking
			return nil
		}
		return err
	}

	if err := json.Unmarshal(data, &s.catalog); err != nil {
		return fmt.Errorf("unmarshal packs catalog: %w", err)
	}
	s.catalogLoaded = true
	debugf("loaded %d entries from pack catalog", len(s.catalog))
	return nil
}

// Delete removes an object. For packed objects, it just removes it from the catalog.
// The actual packfile is not currently garbage collected.
func (s *PackStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Remove from active buffer index
	if _, ok := s.packKeys[key]; ok {
		delete(s.packKeys, key)
		return nil
	}

	// 2. Remove from catalog
	if _, ok := s.catalog[key]; ok {
		delete(s.catalog, key)
		s.catalogDirty = true
		return nil
	}

	// 3. Unpacked objects
	return s.ObjectStore.Delete(ctx, key)
}

func (s *PackStore) Size(ctx context.Context, key string) (int64, error) {
	s.mu.RLock()
	if entry, ok := s.packKeys[key]; ok {
		s.mu.RUnlock()
		return entry.Length, nil
	}
	if entry, ok := s.catalog[key]; ok {
		s.mu.RUnlock()
		return entry.Length, nil
	}
	s.mu.RUnlock()

	return s.ObjectStore.Size(ctx, key)
}

// Repack analyzes the packfiles and repacks those that have too much wasted space.
// Wasted space occurs when objects within a packfile are logically deleted (removed from catalog).
// maxWastedRatio is the threshold (0.0 to 1.0) above which a pack is repacked.
// For example, 0.3 means a pack is repacked if it is more than 30% empty.
// Returns the number of bytes reclaimed, number of packs deleted, and error.
func (s *PackStore) Repack(ctx context.Context, maxWastedRatio float64) (int64, int, error) {
	// Ensure catalog is loaded
	s.mu.Lock()
	if !s.catalogLoaded {
		_ = s.loadCatalogLocked(ctx)
	}
	s.mu.Unlock()

	s.mu.RLock()
	// Calculate active bytes per pack and map keys to packs
	packActiveSizes := make(map[string]int64)
	packToKeys := make(map[string][]string)
	for key, entry := range s.catalog {
		packActiveSizes[entry.PackRef] += entry.Length
		packToKeys[entry.PackRef] = append(packToKeys[entry.PackRef], key)
	}
	s.mu.RUnlock()

	// List all physical packfiles
	packRefs, err := s.ObjectStore.List(ctx, packPrefix)
	if err != nil {
		return 0, 0, fmt.Errorf("list packs: %w", err)
	}

	var bytesReclaimed int64
	var packsDeleted int

	for _, packRef := range packRefs {
		activeSize := packActiveSizes[packRef]

		// 1. Orphaned pack (100% wasted)
		if activeSize == 0 {
			debugf("repack: deleting orphaned pack %s", packRef)
			physicalSize, err := s.ObjectStore.Size(ctx, packRef)
			if err == nil {
				bytesReclaimed += physicalSize
			}
			if err := s.ObjectStore.Delete(ctx, packRef); err != nil {
				return bytesReclaimed, packsDeleted, fmt.Errorf("delete orphaned pack %s: %w", packRef, err)
			}
			packsDeleted++
			s.packCache.Remove(packRef)
			continue
		}

		// 2. Check fragmentation
		physicalSize, err := s.ObjectStore.Size(ctx, packRef)
		if err != nil {
			debugf("repack: failed to get size for pack %s: %v", packRef, err)
			continue
		}

		wasted := physicalSize - activeSize
		if wasted <= 0 {
			continue // No waste or unexpected size
		}

		wastedRatio := float64(wasted) / float64(physicalSize)
		if wastedRatio > maxWastedRatio {
			debugf("repack: repacking %s (wasted: %.2f%%, %d bytes)", packRef, wastedRatio*100, wasted)

			// Download the packfile
			packData, err := s.ObjectStore.Get(ctx, packRef)
			if err != nil {
				return bytesReclaimed, packsDeleted, fmt.Errorf("get pack for repack %s: %w", packRef, err)
			}

			// Re-insert its active objects
			keys := packToKeys[packRef]
			for _, key := range keys {
				s.mu.RLock()
				entry, ok := s.catalog[key]
				s.mu.RUnlock()

				if !ok || entry.PackRef != packRef {
					continue // Catalog changed concurrently? Shouldn't happen during prune, but safe.
				}

				if int64(len(packData)) < entry.Offset+entry.Length {
					return bytesReclaimed, packsDeleted, fmt.Errorf("packfile %s is smaller than expected for key %s", packRef, key)
				}

				data := make([]byte, entry.Length)
				copy(data, packData[entry.Offset:entry.Offset+entry.Length])

				// Put back using PackStore.Put to bundle it into a new pack
				if err := s.Put(ctx, key, data); err != nil {
					return bytesReclaimed, packsDeleted, fmt.Errorf("repack put %s: %w", key, err)
				}
			}

			// Delete the old packfile
			if err := s.ObjectStore.Delete(ctx, packRef); err != nil {
				return bytesReclaimed, packsDeleted, fmt.Errorf("delete old repacked pack %s: %w", packRef, err)
			}

			bytesReclaimed += wasted
			packsDeleted++
			s.packCache.Remove(packRef)
		}
	}

	// Ensure any final repacked objects are flushed
	if err := s.Flush(ctx); err != nil {
		return bytesReclaimed, packsDeleted, fmt.Errorf("flush after repack: %w", err)
	}

	return bytesReclaimed, packsDeleted, nil
}

func (s *PackStore) TotalSize(ctx context.Context) (int64, error) {
	return s.ObjectStore.TotalSize(ctx)
}
