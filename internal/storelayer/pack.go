package storelayer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

const (
	maxPackSize   = 8 * 1024 * 1024 // 8 MB
	maxObjectSize = 512 * 1024      // Only bundle objects smaller than 512 KB
	packPrefix    = "packs/"
	indexPacksKey = "index/packs"
)

// PackStore wraps an store.ObjectStore to aggregate small objects into larger "packfiles".
// It uses a stateless JSON catalog ("index/packs") to keep track of which pack
// contains which object.
type PackStore struct {
	store.ObjectStore

	// log is this store's debug sink, or nil to use the process-wide
	// fallback. Set with WithPackLogger.
	log *logger.Logger

	mu sync.RWMutex

	// Active bundle for small object writes
	packBuffer *bytes.Buffer
	packKeys   map[string]PackEntry

	// The stateless JSON catalog mapped from index/packs
	catalog       map[string]PackEntry
	catalogLoaded bool

	// Set when entries have been removed from the in-memory catalog. Shards are
	// append-only, so a deletion cannot be expressed by writing another one —
	// only compaction, which rewrites the index wholesale, makes it durable.
	needsCompaction bool

	// Index objects whose contents are known to be in the catalog. Compaction
	// removes only these, so a shard written by someone else after this store
	// loaded cannot be deleted unread.
	mergedIndex map[string]bool

	// Key sealing the catalog and pack footers; empty leaves them in plaintext.
	indexKey []byte

	// Entries added since the last shard was written. Flushed as one immutable
	// shard rather than merged into a shared object, so concurrent writers
	// cannot erase each other. See packshard.go.
	pendingShard map[string]PackEntry

	// LRU cache for recently downloaded packfiles to accelerate Get() and HAMT walks
	packCache *lru.Cache[string, []byte]
}

// PackEntry represents the location of a small object within a packfile.
type PackEntry struct {
	PackRef string `json:"p"`
	Offset  int64  `json:"o"`
	Length  int64  `json:"l"`
}

// PackOption configures a PackStore.
type PackOption func(*PackStore)

// WithPackIndexKey seals the pack catalog and packfile footers with key.
//
// PackStore sits below EncryptedStore in the store chain, so the objects it
// writes on its own behalf — index/packs and each pack's footer — never pass
// through the encryption layer. Without a key they are stored in plaintext,
// exposing every packed object's key, its exact ciphertext length, and which
// objects share a pack. Pack *contents* are unaffected either way: they are
// already ciphertext by the time PackStore sees them.
//
// Pass a key derived with crypto.HKDFInfoPackIndexV1, not the master key.
// Repositories without encryption pass none and keep plaintext indexes.
func WithPackIndexKey(key []byte) PackOption {
	return func(s *PackStore) { s.indexKey = key }
}

// NewPackStore initializes a new MicroPackStore over an existing store.ObjectStore.
func NewPackStore(inner store.ObjectStore, opts ...PackOption) (*PackStore, error) {
	// Keep up to 30 MB of packfiles in memory (around 4 packs) to speed up reads
	cache, err := lru.New[string, []byte](4)
	if err != nil {
		return nil, fmt.Errorf("pack cache init: %w", err)
	}

	s := &PackStore{
		ObjectStore:  inner,
		packBuffer:   new(bytes.Buffer),
		packKeys:     make(map[string]PackEntry),
		catalog:      make(map[string]PackEntry),
		pendingShard: make(map[string]PackEntry),
		mergedIndex:  make(map[string]bool),
		packCache:    cache,
	}
	for _, opt := range opts {
		opt(s)
	}
	// Logged after the options are applied so it reaches the sink the caller
	// asked for rather than the process-wide fallback.
	s.debugf("init packstore: LRU size=%d", 4)
	return s, nil
}

// sealIndex encrypts index material when indexKey is set, and passes it through
// unchanged otherwise.
//
// This and openIndex are free functions taking the key explicitly rather than
// methods reading s.indexKey, so that the footer encode/decode path in
// packfooter.go stays independent of PackStore and can be exercised with a key
// of the test's choosing.
func sealIndex(plaintext, indexKey []byte) ([]byte, error) {
	if len(indexKey) == 0 {
		return plaintext, nil
	}
	return crypto.Encrypt(plaintext, indexKey)
}

// openIndex reverses sealIndex. Plaintext input is returned unchanged, which is
// what keeps repositories written before sealing — and repositories with no
// encryption at all — readable.
func openIndex(data, indexKey []byte) ([]byte, error) {
	if !crypto.IsEncrypted(data) {
		return data, nil
	}
	if len(indexKey) == 0 {
		return nil, errors.New("pack index is encrypted but no index key is available")
	}
	return crypto.Decrypt(data, indexKey)
}

// Put stores data either in the active packbuffer or directly to the inner store.
func (s *PackStore) Put(ctx context.Context, key string, data []byte) error {
	// 1. Bypass large objects (e.g. content blocks) and index/packs itself
	if !s.isSmallObject(key, data) {
		return s.ObjectStore.Put(ctx, key, data)
	}

	s.debugf("pack: buffering %s (%d bytes)", key, len(data))

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
	var err error

	// 4. If buffer is full, prepare it for flushing
	if s.packBuffer.Len() >= maxPackSize {
		packRef, packData, err = s.prepareFlushLocked()
	}

	s.mu.Unlock()

	if err != nil {
		return err
	}

	// 5. Upload outside the lock so we don't block concurrent operations
	if packRef != "" {
		if err := s.ObjectStore.Put(ctx, packRef, packData); err != nil {
			s.discardPack(packRef)
			return fmt.Errorf("flush pack %s: %w", packRef, err)
		}
	}

	return nil
}

// discardPack forgets every catalog entry pointing at packRef, for a pack whose
// upload failed.
//
// prepareFlushLocked moves entries into the catalog before the pack exists,
// which is what lets the upload happen outside the lock. If that upload then
// fails, leaving the entries behind is worse than losing them: the catalog
// asserts the objects are stored, so Exists reports true, the next backup
// deduplicates against them and does not re-upload, and a later flush makes the
// claim durable in a shard. The snapshot that results references objects no
// reader can fetch — a backup that reports success and cannot be restored.
//
// Forgetting them instead costs only the re-upload the caller was going to have
// to do anyway, and the pack bytes themselves are content-addressed, so a retry
// that does succeed writes the identical object.
func (s *PackStore) discardPack(packRef string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, entry := range s.catalog {
		if entry.PackRef == packRef {
			delete(s.catalog, key)
		}
	}
	for key, entry := range s.pendingShard {
		if entry.PackRef == packRef {
			delete(s.pendingShard, key)
		}
	}
	s.packCache.Remove(packRef)
	s.debugf("pack: discarded catalog entries for unwritten pack %s", packRef)
}

// packablePrefixes are the content-addressed namespaces safe to bundle into a
// packfile. Every key under them is immutable and named by its own hash, so a
// packed copy can never go stale.
//
// The index/ namespace is deliberately absent: those keys are mutable pointers
// (index/latest), rebuildable catalogs (index/packs, index/snapshots), or locks.
// Packing a mutable key ties its freshness to catalog-flush ordering and leaves
// every superseded copy behind as pack waste.
var packablePrefixes = []string{
	"filemeta/",
	"node/",
	"snapshot/",
	"chunk/",
	"content/",
}

// isSmallObject determines if a key/data pair should be bundled into a packfile.
func (s *PackStore) isSmallObject(key string, data []byte) bool {
	// We only pack metadata and small blobs to keep large data files randomly
	// accessible natively.
	if len(data) > maxObjectSize {
		return false
	}
	for _, prefix := range packablePrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// prepareFlushLocked takes the current buffer, prepares a pack object, updates the catalog,
// and returns the data to be uploaded so it can be done without holding the mutex.
// mu must be locked before calling.
func (s *PackStore) prepareFlushLocked() (string, []byte, error) {
	if s.packBuffer.Len() == 0 {
		return "", nil, nil
	}

	// Append a self-describing footer so the location of every object in this
	// pack can be recovered without the catalog. See RFC 0018.
	footer, err := buildPackFooter(s.packKeys, s.indexKey)
	if err != nil {
		return "", nil, err
	}

	// Copy the buffer since it will be uploaded asynchronously. The footer is
	// part of the object, so the content-addressed name covers it too.
	packData := make([]byte, 0, s.packBuffer.Len()+len(footer))
	packData = append(packData, s.packBuffer.Bytes()...)
	packData = append(packData, footer...)

	packHash := core.ComputeHash(packData)
	packRef := packPrefix + packHash
	s.debugf("preparing packfile %s with %d objects (%d bytes + %d footer)",
		packRef, len(s.packKeys), s.packBuffer.Len(), len(footer))

	// Cache it immediately since we just wrote it, will speed up following Reads
	s.packCache.Add(packRef, packData)

	// Assign the PackRef to all entries that were bundled and move to catalog
	for key, entry := range s.packKeys {
		entry.PackRef = packRef
		s.catalog[key] = entry
		s.pendingShard[key] = entry
	}

	// Reset active buffer
	s.packBuffer.Reset()
	s.packKeys = make(map[string]PackEntry)

	return packRef, packData, nil
}

// Get retrieves an object from the active buffer, a cached pack, or downloads the pack.
func (s *PackStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.RLock()

	// 1. Is it currently in the active memory buffer?
	if entry, ok := s.packKeys[key]; ok {
		data := make([]byte, entry.Length)
		copy(data, s.packBuffer.Bytes()[entry.Offset:entry.Offset+entry.Length])
		s.mu.RUnlock()
		s.debugf("get %s: hit active buffer (len=%d)", key, entry.Length)
		return data, nil
	}

	// 2. Is it in our catalog?
	entry, inCatalog := s.catalog[key]
	s.mu.RUnlock()

	// If not in catalog, wait: we might need to load the catalog from the remote store first,
	// or it's just a normal large object.
	if !inCatalog {
		if key != indexPacksKey && !strings.HasPrefix(key, packPrefix) {
			if err := s.ensureCatalogLoaded(ctx); err != nil {
				return nil, err
			}
			s.mu.RLock()
			entry, inCatalog = s.catalog[key]
			s.mu.RUnlock()
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
		s.debugf("get %s: hit lru pack cache %s (len=%d)", key, entry.PackRef, entry.Length)
		return data, nil
	}

	s.debugf("get %s: downloading pack %s", key, entry.PackRef)
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

	// 2. Add keys from catalog and active buffer matching prefix.
	// An unreadable catalog must fail the listing rather than silently omit
	// every packed key: callers such as prune's mark phase treat a short list
	// as "these objects are unreachable".
	if err := s.ensureCatalogLoaded(ctx); err != nil {
		return nil, err
	}

	s.mu.RLock()
	var packedKeys []string
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
	// The catalog is written back wholesale, so it must first be merged with
	// whatever is already stored. Flushing a catalog that was never loaded
	// would replace the full history with only this session's entries and
	// strand every previously packed object.
	if err := s.ensureCatalogLoaded(ctx); err != nil {
		return fmt.Errorf("refusing to overwrite %s: %w", indexPacksKey, err)
	}

	s.mu.Lock()
	packRef, packData, err := s.prepareFlushLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}

	// Take this flush's entries. They are written as their own immutable shard
	// rather than merged into a shared object, so a concurrent writer cannot
	// erase them. See packshard.go.
	pending := s.pendingShard
	s.pendingShard = make(map[string]PackEntry)
	totalEntries := len(s.catalog)
	s.mu.Unlock()

	if packRef != "" {
		if err := s.ObjectStore.Put(ctx, packRef, packData); err != nil {
			// Restore the entries that belong to packs already written, and
			// drop the ones that belong to this pack — it does not exist, so a
			// shard naming it would point every one of its objects at nothing.
			s.mu.Lock()
			for k, v := range pending {
				if v.PackRef != packRef {
					s.pendingShard[k] = v
				}
			}
			s.mu.Unlock()
			s.discardPack(packRef)
			return fmt.Errorf("flush pack %s: %w", packRef, err)
		}
	}

	if len(pending) > 0 {
		if _, err := s.writeShard(ctx, pending); err != nil {
			s.mu.Lock()
			for k, v := range pending {
				s.pendingShard[k] = v
			}
			s.mu.Unlock()
			return err
		}
		s.debugf("pack: flushed %d new entries (%d total)", len(pending), totalEntries)
	}

	// Shards are append-only, so a removal is durable only once the index is
	// rewritten. Compaction is safe here without a lock because it deletes only
	// what this store has absorbed.
	s.mu.RLock()
	pendingRemoval := s.needsCompaction
	s.mu.RUnlock()
	if pendingRemoval {
		if _, err := s.CompactCatalog(ctx); err != nil {
			return fmt.Errorf("compact pack index: %w", err)
		}
	}

	return nil
}

// loadCatalogLocked fetches the index/packs file from the inner store and populates the catalog.
//
// The catalog is the only record of where a packed object lives, so a failure
// to read it must never be mistaken for "there are no packed objects". Callers
// are required to propagate the error: silently continuing with an empty
// catalog makes every packed object look absent, which is indistinguishable
// from deletion to prune's mark-and-sweep.
//
// s.catalogLoaded is set only when the catalog was read successfully or proven
// absent, and is the invariant Flush relies on before overwriting index/packs.
func (s *PackStore) loadCatalogLocked(ctx context.Context) (err error) {
	// If it was already loaded or proven missing (noted by dirty flag or some other state), we wouldn't be here,
	// but we need to track if we've already attempted loading so we don't spam 404s.
	if s.catalogLoaded {
		return nil
	}

	// Streaming writes directly into s.catalog. If any part of the load fails,
	// remove those uncommitted remote entries before releasing mu while keeping
	// authoritative entries tracked in pendingShard.
	defer func() {
		if err != nil {
			s.resetCatalogAfterLoadFailureLocked()
		}
	}()

	s.debugf("loading pack catalog")
	refs := newPackRefInterner(s.catalog)

	// The monolithic catalog is the pre-shard layout. It is still read so that
	// repositories written before sharding stay readable, but nothing writes it
	// any more; compaction folds it into a shard and removes it.
	legacyEntries, err := s.loadLegacyCatalogLocked(ctx, refs)
	if err != nil {
		return err
	}
	shardEntries, err := s.loadShardsLocked(ctx, refs)
	if err != nil {
		return err
	}

	// The stored index described nothing. On a fresh repository that is correct;
	// on one that has packfiles it means the index was lost, so rebuild from the
	// packs' own footers rather than proceeding as though nothing were packed.
	//
	// The test is on entries the stored index yielded, which is neither of the
	// two things it is tempting to use. len(s.catalog) is wrong because Put's
	// auto-flush populates it with local entries before the index is ever read,
	// so a genuinely lost index stops being detected mid-run. The mere existence
	// of index/packs or a shard is wrong because an object that lists nothing
	// leaves the repository exactly as unindexed as a missing one, and reading it
	// as authoritative turns a recoverable loss into objects reported missing.
	if legacyEntries+shardEntries == 0 {
		if err := s.healMissingCatalogLocked(ctx); err != nil {
			return err
		}
	}

	s.catalogLoaded = true
	s.debugf("loaded %d entries into the pack catalog", len(s.catalog))
	return nil
}

// resetCatalogAfterLoadFailureLocked discards entries streamed from an index
// that did not load completely while retaining independently authoritative
// entries in pendingShard, whether locally written or recovered from a pack
// footer. mu must be held.
func (s *PackStore) resetCatalogAfterLoadFailureLocked() {
	catalog := make(map[string]PackEntry, len(s.pendingShard))
	for key, entry := range s.pendingShard {
		catalog[key] = entry
	}
	s.catalog = catalog
	s.mergedIndex = make(map[string]bool)
}

// loadLegacyCatalogLocked merges the pre-shard monolithic catalog, if the
// repository still has one, and reports how many entries it described. mu must
// be held.
//
// The count, rather than a found/not-found flag, is what loadCatalogLocked needs:
// a catalog object that exists and lists nothing leaves the repository just as
// unindexed as one that was deleted.
func (s *PackStore) loadLegacyCatalogLocked(ctx context.Context, refs packRefInterner) (int, error) {
	// A fresh repository legitimately has no catalog. Establish that up front so
	// a missing object is never conflated with an unreadable one; backends do
	// not expose a typed not-found error, so an existence check is the only
	// reliable way to tell the two apart.
	exists, err := s.ObjectStore.Exists(ctx, indexPacksKey)
	if err != nil {
		return 0, fmt.Errorf("check pack catalog %s: %w", indexPacksKey, err)
	}
	if !exists {
		return 0, nil
	}

	data, err := s.ObjectStore.Get(ctx, indexPacksKey)
	if err != nil {
		// Tolerate a backend that reports absence only on read (or that raced
		// with a concurrent write), but treat every other error as fatal.
		if isNotFoundErr(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("load pack catalog %s: %w", indexPacksKey, err)
	}

	// A catalog written before sealing, or by a repository without encryption,
	// is plaintext and passes through unchanged.
	data, err = openIndex(data, s.indexKey)
	if err != nil {
		return 0, fmt.Errorf("open pack catalog %s: %w", indexPacksKey, err)
	}

	decoded, err := mergePackIndex(data, s.catalog, refs)
	if err != nil {
		return 0, fmt.Errorf("unmarshal packs catalog: %w", err)
	}
	s.mergedIndex[indexPacksKey] = true
	s.debugf("merged %d entries from the legacy catalog", decoded)
	return decoded, nil
}

// isNotFoundErr reports whether err indicates a missing object. Backends do not
// share a typed error, so this matches the shapes they produce in practice.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "NoSuchKey")
}

// ensureCatalogLoaded loads the catalog if it has not been read yet, taking the
// write lock only when a load is actually needed.
func (s *PackStore) ensureCatalogLoaded(ctx context.Context) error {
	s.mu.RLock()
	loaded := s.catalogLoaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Another goroutine may have loaded it while we waited for the lock.
	return s.loadCatalogLocked(ctx)
}

// Delete removes an object. For packed objects, it just removes it from the catalog.
// The actual packfile is not currently garbage collected.
func (s *PackStore) Delete(ctx context.Context, key string) error {
	// Without the catalog a packed key looks unpacked, and the delete would be
	// forwarded to the backend where the object does not physically exist —
	// reporting success while the entry survives the next flush.
	if err := s.ensureCatalogLoaded(ctx); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Remove from active buffer index
	if _, ok := s.packKeys[key]; ok {
		delete(s.packKeys, key)
		return nil
	}

	// 2. Remove from catalog. Shards are append-only, so this is durable only
	// once the index is compacted — see CompactCatalog.
	if _, ok := s.catalog[key]; ok {
		delete(s.catalog, key)
		s.needsCompaction = true
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
	// Ensure catalog is loaded. Repack deletes packfiles whose entries are all
	// gone from the catalog, so an empty catalog would condemn every pack.
	if err := s.ensureCatalogLoaded(ctx); err != nil {
		return 0, 0, err
	}

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

	// Packs whose live objects have been re-inserted but not yet made durable.
	// They are deleted only after the replacement pack and the updated catalog
	// have been flushed, so an interruption leaves recoverable orphans rather
	// than losing the objects.
	type retiredPack struct {
		ref    string
		wasted int64
	}
	var retired []retiredPack

	for _, packRef := range packRefs {
		activeSize := packActiveSizes[packRef]

		// 1. Orphaned pack (100% wasted)
		if activeSize == 0 {
			s.debugf("repack: deleting orphaned pack %s", packRef)
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
			s.debugf("repack: failed to get size for pack %s: %v", packRef, err)
			continue
		}

		// A pack's footer is bookkeeping, not garbage, so it must not count
		// towards waste. Measuring naively first is a safe pre-filter: it can
		// only overstate waste, so a pack that looks healthy by that measure is
		// certainly healthy, and only a pack that looks fragmented is worth
		// spending a footer read to measure accurately.
		wasted := physicalSize - activeSize
		if wasted <= 0 {
			continue // No waste or unexpected size
		}
		if float64(wasted)/float64(physicalSize) > maxWastedRatio {
			if info, ferr := s.readPackFooter(ctx, packRef); ferr == nil {
				physicalSize = info.objectRegionLen
				wasted = physicalSize - activeSize
			} else if !errors.Is(ferr, errNoPackFooter) {
				s.debugf("repack: failed to read footer of %s: %v", packRef, ferr)
			}
		}
		if wasted <= 0 {
			continue
		}

		wastedRatio := float64(wasted) / float64(physicalSize)
		if wastedRatio > maxWastedRatio {
			s.debugf("repack: repacking %s (wasted: %.2f%%, %d bytes)", packRef, wastedRatio*100, wasted)

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

			// The old packfile is retired, not deleted: its contents only become
			// durable at the flush below.
			retired = append(retired, retiredPack{ref: packRef, wasted: wasted})
		}
	}

	// Make the replacement packs and the updated catalog durable before
	// anything they replace is removed.
	if err := s.Flush(ctx); err != nil {
		return bytesReclaimed, packsDeleted, fmt.Errorf("flush after repack: %w", err)
	}

	// Only now is it safe to drop the originals. An error here leaves a
	// superseded pack behind, which the orphan pass above reclaims next run.
	for _, pack := range retired {
		// Packs are named by their content, so rewriting one whose objects are
		// all still live reproduces the identical packfile — and retiring it
		// would delete the very pack the catalog now points at. Deleting a pack
		// is only safe once nothing references it.
		if s.packIsReferenced(pack.ref) {
			s.debugf("repack: keeping %s, still referenced after rewrite", pack.ref)
			continue
		}
		if err := s.ObjectStore.Delete(ctx, pack.ref); err != nil {
			return bytesReclaimed, packsDeleted, fmt.Errorf("delete old repacked pack %s: %w", pack.ref, err)
		}
		bytesReclaimed += pack.wasted
		packsDeleted++
		s.packCache.Remove(pack.ref)
	}

	return bytesReclaimed, packsDeleted, nil
}

// packIsReferenced reports whether any catalog entry still resolves to packRef.
func (s *PackStore) packIsReferenced(packRef string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.catalog {
		if entry.PackRef == packRef {
			return true
		}
	}
	return false
}

func (s *PackStore) TotalSize(ctx context.Context) (int64, error) {
	return s.ObjectStore.TotalSize(ctx)
}
