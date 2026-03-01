package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/cloudstic/cli/internal/core"
	lru "github.com/hashicorp/golang-lru/v2"
	bolt "go.etcd.io/bbolt"
)

const (
	maxPackSize   = 8 * 1024 * 1024 // 8 MB
	maxObjectSize = 512 * 1024      // Only bundle objects smaller than 512 KB
	packPrefix    = "packs/"
	indexPacksKey = "index/packs"
)

var catalogBucket = []byte("c")

// PackStore wraps an ObjectStore to aggregate small objects into larger "packfiles".
// It uses a stateless JSON catalog ("index/packs") to keep track of which pack
// contains which object. The local catalog is kept in a temporary bbolt database
// to avoid holding potentially large maps on the Go heap.
type PackStore struct {
	ObjectStore

	mu sync.RWMutex

	// Active bundle for small object writes (bounded by maxPackSize, stays in memory)
	packBuffer *bytes.Buffer
	packKeys   map[string]PackEntry

	// bbolt-backed catalog for packed object locations
	catalogDB     *bolt.DB
	catalogDBPath string
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
	cache, err := lru.New[string, []byte](4)
	if err != nil {
		return nil, fmt.Errorf("pack cache init: %w", err)
	}

	f, err := os.CreateTemp("", "cloudstic-packcatalog-*.db")
	if err != nil {
		return nil, fmt.Errorf("pack catalog temp file: %w", err)
	}
	dbPath := f.Name()
	_ = f.Close()

	db, err := bolt.Open(dbPath, 0600, &bolt.Options{NoSync: true, NoFreelistSync: true})
	if err != nil {
		_ = os.Remove(dbPath)
		return nil, fmt.Errorf("pack catalog bolt open: %w", err)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(catalogBucket)
		return err
	}); err != nil {
		_ = db.Close()
		_ = os.Remove(dbPath)
		return nil, fmt.Errorf("pack catalog create bucket: %w", err)
	}

	return &PackStore{
		ObjectStore:   inner,
		packBuffer:    new(bytes.Buffer),
		packKeys:      make(map[string]PackEntry),
		catalogDB:     db,
		catalogDBPath: dbPath,
		packCache:     cache,
	}, nil
}

// Close releases the bbolt catalog database and removes the temp file.
func (s *PackStore) Close() error {
	if s.catalogDB != nil {
		_ = s.catalogDB.Close()
	}
	if s.catalogDBPath != "" {
		_ = os.Remove(s.catalogDBPath)
	}
	return nil
}

func (s *PackStore) catalogGet(key string) (PackEntry, bool) {
	var entry PackEntry
	var found bool
	_ = s.catalogDB.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(catalogBucket).Get([]byte(key))
		if v != nil {
			if err := json.Unmarshal(v, &entry); err == nil {
				found = true
			}
		}
		return nil
	})
	return entry, found
}

func (s *PackStore) catalogPutBatch(entries map[string]PackEntry) {
	_ = s.catalogDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(catalogBucket)
		for key, entry := range entries {
			v, _ := json.Marshal(entry)
			if err := b.Put([]byte(key), v); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *PackStore) catalogDelete(key string) {
	_ = s.catalogDB.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(catalogBucket).Delete([]byte(key))
	})
}

func (s *PackStore) catalogHas(key string) bool {
	var found bool
	_ = s.catalogDB.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(catalogBucket).Get([]byte(key)) != nil
		return nil
	})
	return found
}

// catalogKeysWithPrefix returns all keys in the catalog that start with prefix.
func (s *PackStore) catalogKeysWithPrefix(prefix string) []string {
	var keys []string
	pfx := []byte(prefix)
	_ = s.catalogDB.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(catalogBucket).Cursor()
		for k, _ := c.Seek(pfx); k != nil && bytes.HasPrefix(k, pfx); k, _ = c.Next() {
			keys = append(keys, string(k))
		}
		return nil
	})
	return keys
}

// catalogAll returns all catalog entries as a map (used for serialization and repack analysis).
func (s *PackStore) catalogAll() map[string]PackEntry {
	result := make(map[string]PackEntry)
	_ = s.catalogDB.View(func(tx *bolt.Tx) error {
		return tx.Bucket(catalogBucket).ForEach(func(k, v []byte) error {
			var entry PackEntry
			if err := json.Unmarshal(v, &entry); err == nil {
				result[string(k)] = entry
			}
			return nil
		})
	})
	return result
}

// Put stores data either in the active packbuffer or directly to the inner store.
func (s *PackStore) Put(ctx context.Context, key string, data []byte) error {
	if !s.isSmallObject(key, data) {
		return s.ObjectStore.Put(ctx, key, data)
	}

	s.mu.Lock()

	offset := int64(s.packBuffer.Len())
	s.packBuffer.Write(data)

	s.packKeys[key] = PackEntry{
		Offset: offset,
		Length: int64(len(data)),
	}

	var packRef string
	var packData []byte

	if s.packBuffer.Len() >= maxPackSize {
		packRef, packData = s.prepareFlushLocked()
	}

	s.mu.Unlock()

	if packRef != "" {
		if err := s.ObjectStore.Put(ctx, packRef, packData); err != nil {
			return fmt.Errorf("flush pack %s: %w", packRef, err)
		}
	}

	return nil
}

// isSmallObject determines if a key/data pair should be bundled into a packfile.
func (s *PackStore) isSmallObject(key string, data []byte) bool {
	if key == indexPacksKey || strings.HasPrefix(key, "index/lock") {
		return false
	}
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

	packHash := core.ComputeHash(s.packBuffer.Bytes())
	packRef := packPrefix + packHash
	debugf("preparing packfile %s with %d objects (%d bytes)", packRef, len(s.packKeys), s.packBuffer.Len())

	packData := make([]byte, s.packBuffer.Len())
	copy(packData, s.packBuffer.Bytes())

	s.packCache.Add(packRef, packData)

	batch := make(map[string]PackEntry, len(s.packKeys))
	for key, entry := range s.packKeys {
		entry.PackRef = packRef
		batch[key] = entry
	}
	s.catalogPutBatch(batch)
	s.catalogDirty = true

	s.packBuffer.Reset()
	s.packKeys = make(map[string]PackEntry)

	return packRef, packData
}

// Get retrieves an object from the active buffer, a cached pack, or downloads the pack.
func (s *PackStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.RLock()

	if entry, ok := s.packKeys[key]; ok {
		data := make([]byte, entry.Length)
		copy(data, s.packBuffer.Bytes()[entry.Offset:entry.Offset+entry.Length])
		s.mu.RUnlock()
		debugf("get %s: hit active buffer (len=%d)", key, entry.Length)
		return data, nil
	}
	s.mu.RUnlock()

	entry, inCatalog := s.catalogGet(key)

	if !inCatalog {
		if key != indexPacksKey && !strings.HasPrefix(key, packPrefix) {
			s.mu.Lock()
			if !s.catalogLoaded {
				_ = s.loadCatalogLocked(ctx)
			}
			s.mu.Unlock()
			entry, inCatalog = s.catalogGet(key)
		}

		if !inCatalog {
			return s.ObjectStore.Get(ctx, key)
		}
	}

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
	s.mu.RUnlock()

	if s.catalogHas(key) {
		return true, nil
	}

	return s.ObjectStore.Exists(ctx, key)
}

// List returns all keys matching the prefix, merging results from the inner store
// with the keys currently buffered or indexed in packfiles.
func (s *PackStore) List(ctx context.Context, prefix string) ([]string, error) {
	innerKeys, err := s.ObjectStore.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if !s.catalogLoaded {
		_ = s.loadCatalogLocked(ctx)
	}
	s.mu.Unlock()

	catalogKeys := s.catalogKeysWithPrefix(prefix)

	s.mu.RLock()
	var bufferKeys []string
	for key := range s.packKeys {
		if strings.HasPrefix(key, prefix) {
			bufferKeys = append(bufferKeys, key)
		}
	}
	s.mu.RUnlock()

	keySet := make(map[string]struct{}, len(innerKeys)+len(catalogKeys)+len(bufferKeys))
	for _, k := range innerKeys {
		if strings.HasPrefix(k, packPrefix) || k == indexPacksKey {
			continue
		}
		keySet[k] = struct{}{}
	}
	for _, k := range catalogKeys {
		keySet[k] = struct{}{}
	}
	for _, k := range bufferKeys {
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
	if s.catalogDirty {
		all := s.catalogAll()
		var err error
		catalogBytes, err = json.Marshal(all)
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
		s.mu.Unlock()
	}

	return nil
}

// loadCatalogLocked fetches the index/packs file from the inner store and populates the catalog.
func (s *PackStore) loadCatalogLocked(ctx context.Context) error {
	if s.catalogLoaded {
		return nil
	}

	debugf("loading pack catalog from %s", indexPacksKey)
	data, err := s.ObjectStore.Get(ctx, indexPacksKey)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "NoSuchKey") {
			s.catalogLoaded = true
			return nil
		}
		return err
	}

	var catalog map[string]PackEntry
	if err := json.Unmarshal(data, &catalog); err != nil {
		return fmt.Errorf("unmarshal packs catalog: %w", err)
	}

	s.catalogPutBatch(catalog)
	s.catalogLoaded = true
	debugf("loaded %d entries from pack catalog", len(catalog))
	return nil
}

// Delete removes an object. For packed objects, it just removes it from the catalog.
// The actual packfile is not currently garbage collected.
func (s *PackStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()

	if _, ok := s.packKeys[key]; ok {
		delete(s.packKeys, key)
		s.mu.Unlock()
		return nil
	}

	s.mu.Unlock()

	if s.catalogHas(key) {
		s.catalogDelete(key)
		s.mu.Lock()
		s.catalogDirty = true
		s.mu.Unlock()
		return nil
	}

	return s.ObjectStore.Delete(ctx, key)
}

func (s *PackStore) Size(ctx context.Context, key string) (int64, error) {
	s.mu.RLock()
	if entry, ok := s.packKeys[key]; ok {
		s.mu.RUnlock()
		return entry.Length, nil
	}
	s.mu.RUnlock()

	if entry, ok := s.catalogGet(key); ok {
		return entry.Length, nil
	}

	return s.ObjectStore.Size(ctx, key)
}

// Repack analyzes the packfiles and repacks those that have too much wasted space.
// Wasted space occurs when objects within a packfile are logically deleted (removed from catalog).
// maxWastedRatio is the threshold (0.0 to 1.0) above which a pack is repacked.
// For example, 0.3 means a pack is repacked if it is more than 30% empty.
// Returns the number of bytes reclaimed, number of packs deleted, and error.
func (s *PackStore) Repack(ctx context.Context, maxWastedRatio float64) (int64, int, error) {
	s.mu.Lock()
	if !s.catalogLoaded {
		_ = s.loadCatalogLocked(ctx)
	}
	s.mu.Unlock()

	allEntries := s.catalogAll()

	packActiveSizes := make(map[string]int64)
	packToKeys := make(map[string][]string)
	for key, entry := range allEntries {
		packActiveSizes[entry.PackRef] += entry.Length
		packToKeys[entry.PackRef] = append(packToKeys[entry.PackRef], key)
	}

	packRefs, err := s.ObjectStore.List(ctx, packPrefix)
	if err != nil {
		return 0, 0, fmt.Errorf("list packs: %w", err)
	}

	var bytesReclaimed int64
	var packsDeleted int

	for _, packRef := range packRefs {
		activeSize := packActiveSizes[packRef]

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

		physicalSize, err := s.ObjectStore.Size(ctx, packRef)
		if err != nil {
			debugf("repack: failed to get size for pack %s: %v", packRef, err)
			continue
		}

		wasted := physicalSize - activeSize
		if wasted <= 0 {
			continue
		}

		wastedRatio := float64(wasted) / float64(physicalSize)
		if wastedRatio > maxWastedRatio {
			debugf("repack: repacking %s (wasted: %.2f%%, %d bytes)", packRef, wastedRatio*100, wasted)

			packData, err := s.ObjectStore.Get(ctx, packRef)
			if err != nil {
				return bytesReclaimed, packsDeleted, fmt.Errorf("get pack for repack %s: %w", packRef, err)
			}

			keys := packToKeys[packRef]
			for _, key := range keys {
				entry, ok := s.catalogGet(key)
				if !ok || entry.PackRef != packRef {
					continue
				}

				if int64(len(packData)) < entry.Offset+entry.Length {
					return bytesReclaimed, packsDeleted, fmt.Errorf("packfile %s is smaller than expected for key %s", packRef, key)
				}

				data := make([]byte, entry.Length)
				copy(data, packData[entry.Offset:entry.Offset+entry.Length])

				if err := s.Put(ctx, key, data); err != nil {
					return bytesReclaimed, packsDeleted, fmt.Errorf("repack put %s: %w", key, err)
				}
			}

			if err := s.ObjectStore.Delete(ctx, packRef); err != nil {
				return bytesReclaimed, packsDeleted, fmt.Errorf("delete old repacked pack %s: %w", packRef, err)
			}

			bytesReclaimed += wasted
			packsDeleted++
			s.packCache.Remove(packRef)
		}
	}

	if err := s.Flush(ctx); err != nil {
		return bytesReclaimed, packsDeleted, fmt.Errorf("flush after repack: %w", err)
	}

	return bytesReclaimed, packsDeleted, nil
}

func (s *PackStore) TotalSize(ctx context.Context) (int64, error) {
	return s.ObjectStore.TotalSize(ctx)
}
