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
	catalog       *packCatalog
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

	// Keys added to the catalog since the last shard was written, flushed as one
	// immutable shard rather than merged into a shared object so that concurrent
	// writers cannot erase each other. See packshard.go.
	//
	// A key set rather than a second copy of the entries: the values are already
	// in catalog, and holding them twice cost a whole duplicate of a run's worth
	// of entries -- the largest single allocation in a backup. The strings are
	// shared with catalog's keys, so an entry costs a header and a map slot.
	//
	// It also records which catalog entries are authoritative. loadCatalogLocked
	// streams remote entries straight into catalog, so a failed load can leave
	// entries behind; these are the ones that survive it, because nothing but a
	// local write or a footer rebuild puts a key here.
	pendingKeys map[string]struct{}

	// LRU cache for recently downloaded packfiles to accelerate Get() and HAMT walks
	packCache *packBodyCache

	// Misses per pack since it was last cached, used to decide when reading one
	// object at a time is no longer the cheaper option. See resolveFromPack.
	packMisses *lru.Cache[string, int]

	// Hits served from a pack since it was promoted into packCache, read by
	// onPackEvicted to tell a promotion that paid for itself from one that
	// didn't. See packPenalized.
	packHits *lru.Cache[string, int]

	// Packs whose last promotion was evicted before serving packPromoteAfter
	// hits — cheaper to have left as ranged reads. A pack in here is never
	// promoted again until it ages out of this bounded structure, which is
	// what keeps a working set bigger than packCache from paying a whole-pack
	// transfer over and over: the cost per object converges to a ranged read,
	// bounded by object size, rather than staying bounded by pack size no
	// matter how much the true working set overruns the cache. See
	// onPackEvicted and resolveFromPack.
	packPenalized *lru.Cache[string, struct{}]
}

// PackEntry represents the location of a small object within a packfile.
type PackEntry struct {
	PackRef string `json:"p"`
	Offset  int64  `json:"o"`
	Length  int64  `json:"l"`
}

// PackOption configures a PackStore.
type PackOption func(*PackStore)

// withPackBodyCacheBudget overrides the pack body cache's byte budget.
//
// Unexported: production code has one number, arrived at by measurement (see
// packBodyCacheBudget), and this exists only so a test can put a working set
// through a cache deliberately smaller than it, without needing a repository
// large enough to do that with the real budget.
func withPackBodyCacheBudget(budget int) PackOption {
	return func(s *PackStore) {
		cache, err := newPackBodyCache(budget, s.onPackEvicted)
		if err != nil {
			panic(err) // test-only option; a bad budget here is a test bug
		}
		s.packCache = cache
	}
}

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
	misses, err := lru.New[string, int](packMissWindow)
	if err != nil {
		return nil, fmt.Errorf("pack miss counter init: %w", err)
	}
	hits, err := lru.New[string, int](packMissWindow)
	if err != nil {
		return nil, fmt.Errorf("pack hit counter init: %w", err)
	}
	penalized, err := lru.New[string, struct{}](packMissWindow)
	if err != nil {
		return nil, fmt.Errorf("pack penalty tracker init: %w", err)
	}

	s := &PackStore{
		ObjectStore:   inner,
		packBuffer:    new(bytes.Buffer),
		packKeys:      make(map[string]PackEntry),
		catalog:       newPackCatalog(),
		pendingKeys:   make(map[string]struct{}),
		mergedIndex:   make(map[string]bool),
		packMisses:    misses,
		packHits:      hits,
		packPenalized: penalized,
	}
	// s.onPackEvicted needs packHits and packPenalized, both set above, so the
	// cache is built after them and assigned rather than passed in the struct
	// literal.
	cache, err := newPackBodyCache(packBodyCacheBudget, s.onPackEvicted)
	if err != nil {
		return nil, fmt.Errorf("pack cache init: %w", err)
	}
	s.packCache = cache

	for _, opt := range opts {
		opt(s)
	}
	// Logged after the options are applied so it reaches the sink the caller
	// asked for rather than the process-wide fallback.
	s.debugf("init packstore: pack body budget=%d bytes", packBodyCacheBudget)
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

	var stale []string
	s.catalog.Each(func(key string, entry PackEntry) {
		if entry.PackRef == packRef {
			stale = append(stale, key)
		}
	})
	for _, key := range stale {
		s.catalog.Delete(key)
	}
	for key := range s.pendingKeys {
		if entry, ok := s.catalog.Get(key); !ok || entry.PackRef == packRef {
			delete(s.pendingKeys, key)
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
		s.catalog.Set(key, entry)
		s.pendingKeys[key] = struct{}{}
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
	entry, inCatalog := s.catalog.Get(key)
	s.mu.RUnlock()

	// If not in catalog, wait: we might need to load the catalog from the remote store first,
	// or it's just a normal large object.
	if !inCatalog {
		if key != indexPacksKey && !strings.HasPrefix(key, packPrefix) {
			if err := s.ensureCatalogLoaded(ctx); err != nil {
				return nil, err
			}
			s.mu.RLock()
			entry, inCatalog = s.catalog.Get(key)
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
		s.recordPackHit(entry.PackRef)
		s.debugf("get %s: hit lru pack cache %s (len=%d)", key, entry.PackRef, entry.Length)
		return data, nil
	}

	return s.resolveFromPack(ctx, key, entry)
}

// recordPackHit counts one object served from a cached pack, read back by
// onPackEvicted when that pack leaves the cache.
func (s *PackStore) recordPackHit(packRef string) {
	hits := 0
	if n, ok := s.packHits.Get(packRef); ok {
		hits = n
	}
	s.packHits.Add(packRef, hits+1)
}

// onPackEvicted runs when a pack body is dropped because the cache ran out of
// room — not when one is removed deliberately, which carries no such meaning
// (see newPackBodyCache).
//
// A promotion (see resolveFromPack) pays for a whole-pack transfer up front,
// betting it back on the hits the pack serves while cached. That bet loses
// when the working set is bigger than the cache: the pack is evicted to make
// room for another before it has served enough hits to be worth what it cost,
// and the next miss on the same pack would promote it again, pay the same
// transfer, and lose the same bet — forever, at a cost bounded by pack size no
// matter how much larger the true working set is than the cache.
//
// Recording the loss here is what breaks that cycle. A pack that didn't earn
// back its promotion is marked in packPenalized, and resolveFromPack serves it
// with ranged reads from then on rather than promoting it again — bounding the
// cost per object by object size instead, which holds regardless of how large
// the repository or how mismatched the cache is to it. The mark expires when
// it ages out of packPenalized's own bounded window, so a pack that becomes
// worth caching again — the working set shrinks, or access moves on — gets
// another chance rather than being penalized permanently on one bad measurement.
func (s *PackStore) onPackEvicted(packRef string) {
	hits := 0
	if n, ok := s.packHits.Get(packRef); ok {
		hits = n
	}
	s.packHits.Remove(packRef)
	if hits < packPromoteAfter {
		s.packPenalized.Add(packRef, struct{}{})
	}
}

const (
	// packPromoteAfter is how many times a pack may be read one object at a time
	// before the whole thing is fetched and cached instead.
	//
	// The two access patterns this sits between were measured (RFC 0023). A scan
	// -- check, ls, prune -- walks a pack's objects consecutively, so one transfer
	// serves thousands of reads and ranged reads would be thousands of round
	// trips instead of one. A scattered read -- restore's write phase, or a cat
	// of a single file -- touches a pack once or twice, so fetching up to
	// maxPackSize to return a few hundred bytes is nearly all waste.
	//
	// The threshold is asymmetric in what it costs each pattern, which is why it
	// is not 2. A scan pays at most packPromoteAfter-1 extra small requests per
	// pack visit -- a few hundred across a repository, against transfers it makes
	// anyway. A scattered read pays one whole pack per packPromoteAfter misses,
	// which is bytes, and bytes are what a remote backend bills for.
	//
	// Measured on a 51-pack tree, restore's whole-pack transfers against this
	// value: 2857 at 2, 1419 at 4, 719 at 8, 358 at 16, 179 at 32. Most of the
	// benefit is captured by 8, and beyond it the round trips traded away start
	// to matter more than the bytes saved.
	packPromoteAfter = 8

	// packMissWindow bounds the miss counter, the hit counter and the penalty
	// set. Each only has to span the packs in flight around a given moment, and
	// none may become a second unbounded per-repository structure -- which is
	// the thing RFC 0023 is about.
	//
	// It has to stay comfortably above the number of packs the body cache can
	// hold (packBodyCacheBudget / maxPackSize, currently 8). A hit counter
	// evicted from this window while its pack is still resident would read as
	// zero hits on the next eviction and penalize a pack that was serving
	// fine.
	packMissWindow = 64
)

// rangesNatively reports whether a ranged read on s reaches a backend that
// serves it as a ranged read, rather than one that emulates it by transferring
// the whole object and slicing.
//
// Implementing store.RangeGetter is not the same claim. DebugStore declares the
// method unconditionally and falls back to a full Get over an inner store that
// cannot range, which is right for the footer path — there the fallback costs
// exactly what not ranging would have cost anyway. It is wrong here, because
// this decides *whether* to range: emulated ranging would transfer the whole
// pack on every one of the first packPromoteAfter misses and never populate the
// body cache, which is worse than not ranging at all.
//
// So walk to the bottom of the wrapper chain and ask the backend itself.
func rangesNatively(s store.ObjectStore) bool {
	for {
		u, ok := s.(store.Unwrapper)
		if !ok {
			break
		}
		inner := u.Unwrap()
		if inner == nil {
			break
		}
		s = inner
	}
	_, ok := s.(store.RangeGetter)
	return ok
}

// resolveFromPack returns one object from a pack that is not in the body cache.
//
// Reading the whole pack to return a slice of it is right when the pack is being
// scanned and wrong when it is being sampled, and the two are indistinguishable
// at the first miss. So the first misses are served by ranged reads, and a pack
// that keeps missing is fetched whole and cached -- see packPromoteAfter. A pack
// whose last promotion was evicted before earning it back stays on ranged reads
// regardless of miss count -- see packPenalized and onPackEvicted.
func (s *PackStore) resolveFromPack(ctx context.Context, key string, entry PackEntry) ([]byte, error) {
	ranger, canRange := s.ObjectStore.(store.RangeGetter)
	canRange = canRange && rangesNatively(s.ObjectStore)

	misses := 0
	if n, ok := s.packMisses.Get(entry.PackRef); ok {
		misses = n
	}
	misses++
	s.packMisses.Add(entry.PackRef, misses)

	// Peek, not Get: checking the penalty must not refresh its recency, or a
	// pack read every miss (which a penalized one is, by definition) would
	// keep itself permanently fresh in packPenalized and never age out for
	// the second chance the comment on that field promises.
	_, penalized := s.packPenalized.Peek(entry.PackRef)

	if canRange && (misses < packPromoteAfter || penalized) {
		data, err := ranger.GetRange(ctx, entry.PackRef, entry.Offset, entry.Length)
		if err != nil {
			return nil, fmt.Errorf("read %s from pack %s: %w", key, entry.PackRef, err)
		}
		if int64(len(data)) != entry.Length {
			return nil, fmt.Errorf("ranged read of %s from pack %s returned %d bytes, want %d",
				key, entry.PackRef, len(data), entry.Length)
		}
		s.debugf("get %s: ranged read from pack %s (len=%d)", key, entry.PackRef, entry.Length)
		return data, nil
	}

	s.debugf("get %s: downloading pack %s (miss %d)", key, entry.PackRef, misses)
	packData, err := s.ObjectStore.Get(ctx, entry.PackRef)
	if err != nil {
		return nil, fmt.Errorf("fetch pack %s for key %s: %w", entry.PackRef, key, err)
	}

	s.packCache.Add(entry.PackRef, packData)
	// The pack is cached now, so the misses that led here are spent.
	s.packMisses.Remove(entry.PackRef)

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
	if s.catalog.Has(key) {
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
	s.catalog.Each(func(key string, _ PackEntry) {
		if strings.HasPrefix(key, prefix) {
			packedKeys = append(packedKeys, key)
		}
	})
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
	pending := s.pendingKeys
	s.pendingKeys = make(map[string]struct{})
	totalEntries := s.catalog.Len()
	// Seal here, under the lock that already guards catalog, so the shard is
	// rendered straight from it. Materialising the pending entries into a map
	// first would reinstate the duplicate this change removes — pending is a
	// whole run's worth of entries by the time a backup flushes.
	shardRef, shardData, sealErr := s.sealShardFor(s.catalog, pending)
	s.mu.Unlock()

	restorePending := func() {
		s.mu.Lock()
		for k := range pending {
			s.pendingKeys[k] = struct{}{}
		}
		s.mu.Unlock()
	}

	if packRef != "" {
		if err := s.ObjectStore.Put(ctx, packRef, packData); err != nil {
			// Restore the keys that belong to packs already written; discardPack
			// drops the ones belonging to this pack, which does not exist, so a
			// shard naming it would point every one of its objects at nothing.
			restorePending()
			s.discardPack(packRef)
			return fmt.Errorf("flush pack %s: %w", packRef, err)
		}
	}

	if sealErr != nil {
		restorePending()
		return sealErr
	}
	if shardRef != "" {
		if err := s.ObjectStore.Put(ctx, shardRef, shardData); err != nil {
			restorePending()
			return fmt.Errorf("write pack index shard %s: %w", shardRef, err)
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
	// authoritative entries named by pendingKeys.
	defer func() {
		if err != nil {
			s.resetCatalogAfterLoadFailureLocked()
		}
	}()

	s.debugf("loading pack catalog")

	// The monolithic catalog is the pre-shard layout. It is still read so that
	// repositories written before sharding stay readable, but nothing writes it
	// any more; compaction folds it into a shard and removes it.
	legacyEntries, err := s.loadLegacyCatalogLocked(ctx)
	if err != nil {
		return err
	}
	shardEntries, err := s.loadShardsLocked(ctx)
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
	s.debugf("loaded %d entries into the pack catalog", s.catalog.Len())
	return nil
}

// resetCatalogAfterLoadFailureLocked discards entries streamed from an index
// that did not load completely while retaining independently authoritative
// entries named by pendingKeys, whether locally written or recovered from a pack
// footer. mu must be held.
func (s *PackStore) resetCatalogAfterLoadFailureLocked() {
	catalog := newPackCatalog()
	for key := range s.pendingKeys {
		if entry, ok := s.catalog.Get(key); ok {
			catalog.Set(key, entry)
		}
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
func (s *PackStore) loadLegacyCatalogLocked(ctx context.Context) (int, error) {
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

	decoded, err := mergePackIndex(data, s.catalog)
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
	if s.catalog.Has(key) {
		s.catalog.Delete(key)
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
	if entry, ok := s.catalog.Get(key); ok {
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
	s.catalog.Each(func(key string, entry PackEntry) {
		packActiveSizes[entry.PackRef] += entry.Length
		packToKeys[entry.PackRef] = append(packToKeys[entry.PackRef], key)
	})
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
				entry, ok := s.catalog.Get(key)
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
	referenced := false
	s.catalog.Each(func(_ string, entry PackEntry) {
		if entry.PackRef == packRef {
			referenced = true
		}
	})
	return referenced
}

func (s *PackStore) TotalSize(ctx context.Context) (int64, error) {
	return s.ObjectStore.TotalSize(ctx)
}
