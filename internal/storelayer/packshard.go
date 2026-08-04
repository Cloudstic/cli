package storelayer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/cloudstic/cli/internal/core"
)

// The pack catalog is stored as append-only shards rather than one mutable
// object.
//
// A single index/packs object had to be read, modified, and written back on
// every flush. Backups take shared locks by design, so two runs would both load
// the same catalog, append their own entries, and write back — last writer wins,
// and the loser's entries became unaddressable. Pack offsets exist nowhere else,
// so unlike index/snapshots that loss could not be reconciled by listing.
//
// Each flush now writes its own immutable shard and never touches another, so
// concurrent writers cannot erase each other. Readers merge every shard. The
// merge is idempotent and order-independent: keys are content-addressed, so a
// key appearing in two shards names byte-identical content and either location
// is correct.
//
// Shards live under index/packmap/ rather than index/packs/ deliberately. Keys
// map to filesystem paths on LocalStore, where a file named index/packs cannot
// coexist with a directory of the same name — and the legacy object has to stay
// readable.
//
// See RFC 0018 and docs/compatibility.md.
const shardPrefix = "index/packmap/"

// mergePackIndex decodes one JSON index object directly into catalog. Decoding
// an entry before deciding whether to insert it keeps duplicate keys harmless,
// while avoiding an intermediate map proportional to the whole shard.
//
// It reports how many entries the object contained, not how many were inserted.
// The two differ whenever a key is already present, and the caller's question —
// did the stored index describe anything — is answered by the former. Counting
// insertions instead would read a healthy index as empty whenever the entries it
// names were already in the catalog, which is what auto-flush leaves behind.
func mergePackIndex(data []byte, catalog *packCatalog) (int, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	start, err := dec.Token()
	if err != nil {
		return 0, err
	}
	if start != json.Delim('{') {
		return 0, fmt.Errorf("pack index must be a JSON object")
	}

	decoded := 0
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return decoded, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return decoded, fmt.Errorf("pack index key is not a string")
		}

		var entry PackEntry
		if err := dec.Decode(&entry); err != nil {
			return decoded, err
		}
		decoded++
		if catalog.Has(key) {
			continue
		}
		catalog.Set(key, entry)
	}

	end, err := dec.Token()
	if err != nil {
		return decoded, err
	}
	if end != json.Delim('}') {
		return decoded, fmt.Errorf("pack index is missing its closing object delimiter")
	}

	// json.Unmarshal rejects trailing values, so retain that validation while
	// using the streaming decoder.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return decoded, fmt.Errorf("pack index has trailing data")
		}
		return decoded, err
	}
	return decoded, nil
}

// sealShard renders entries as the object a shard is stored as, returning the
// key it belongs under and the bytes to write. An empty set has no shard and
// returns an empty key.
//
// It performs no I/O, so a caller holding mu can seal the catalog in place and
// release the lock before writing. That is what lets CompactCatalog avoid
// copying the whole catalog purely to get it out from under the lock.
func (s *PackStore) sealShard(entries map[string]PackEntry) (string, []byte, error) {
	if len(entries) == 0 {
		return "", nil, nil
	}

	plain, err := json.Marshal(entries)
	if err != nil {
		return "", nil, fmt.Errorf("marshal pack index shard: %w", err)
	}
	sealed, err := sealIndex(plain, s.indexKey)
	if err != nil {
		return "", nil, fmt.Errorf("seal pack index shard: %w", err)
	}

	// Name by the plaintext hash so the key is stable regardless of the nonce a
	// sealed shard carries.
	return shardPrefix + core.ComputeHash(plain), sealed, nil
}

// sealShardFor renders a shard containing exactly the catalog entries named by
// keys, without building a map of them first.
//
// json.Marshal of a map sorts its keys, so the entries are sorted here too and
// the bytes — and therefore the content-addressed shard key — are identical to
// what marshalling an equivalent map would produce.
//
// The caller must hold mu: catalog and keys are both read here.
func (s *PackStore) sealShardFor(catalog *packCatalog, keys map[string]struct{}) (string, []byte, error) {
	if len(keys) == 0 {
		return "", nil, nil
	}

	ordered := make([]string, 0, len(keys))
	for key := range keys {
		if catalog.Has(key) {
			ordered = append(ordered, key)
		}
	}
	if len(ordered) == 0 {
		return "", nil, nil
	}
	sort.Strings(ordered)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range ordered {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return "", nil, fmt.Errorf("marshal pack index shard key: %w", err)
		}
		buf.Write(name)
		buf.WriteByte(':')
		located, _ := catalog.Get(key)
		entry, err := json.Marshal(located)
		if err != nil {
			return "", nil, fmt.Errorf("marshal pack index shard entry: %w", err)
		}
		buf.Write(entry)
	}
	buf.WriteByte('}')

	plain := buf.Bytes()
	sealed, err := sealIndex(plain, s.indexKey)
	if err != nil {
		return "", nil, fmt.Errorf("seal pack index shard: %w", err)
	}
	return shardPrefix + core.ComputeHash(plain), sealed, nil
}

// writeShard persists entries as a new immutable shard and returns the key it
// wrote, or an empty key when there was nothing to write. Two runs that happen
// to produce identical shards write the same object, which is harmless.
//
// The key is returned rather than recomputed by the caller. Deriving it a second
// time meant marshalling the whole map again, and a caller that derived it
// differently — from a marshal that failed, say — would name an object that was
// never written and then delete the shards it was meant to replace.
func (s *PackStore) writeShard(ctx context.Context, entries map[string]PackEntry) (string, error) {
	key, sealed, err := s.sealShard(entries)
	if err != nil || key == "" {
		return "", err
	}

	if err := s.ObjectStore.Put(ctx, key, sealed); err != nil {
		return "", fmt.Errorf("write pack index shard %s: %w", key, err)
	}
	s.debugf("pack: wrote shard %s with %d entries", key, len(entries))
	return key, nil
}

// loadShardsLocked merges every shard into the in-memory catalog and returns
// how many entries those shards described. mu must be held.
//
// The count is of entries rather than of shards because it feeds the decision in
// loadCatalogLocked about whether the stored index is lost. A shard that exists
// and describes nothing leaves the catalog exactly as empty as no shard at all.
//
// A shard that cannot be read fails the caller. Continuing with a partial merge
// would produce a catalog that looks complete and is not, which is how a prune
// deletes objects it simply could not see.
func (s *PackStore) loadShardsLocked(ctx context.Context) (int, error) {
	keys, err := s.ObjectStore.List(ctx, shardPrefix)
	if err != nil {
		return 0, fmt.Errorf("list pack index shards: %w", err)
	}

	entries := 0
	for _, key := range keys {
		data, err := s.ObjectStore.Get(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("read pack index shard %s: %w", key, err)
		}
		plain, err := openIndex(data, s.indexKey)
		if err != nil {
			return 0, fmt.Errorf("open pack index shard %s: %w", key, err)
		}

		decoded, err := mergePackIndex(plain, s.catalog)
		if err != nil {
			return 0, fmt.Errorf("unmarshal pack index shard %s: %w", key, err)
		}
		entries += decoded
		s.mergedIndex[key] = true
	}

	if len(keys) > 0 {
		s.debugf("pack: merged %d entries from %d shards", entries, len(keys))
	}
	return entries, nil
}

// CompactCatalog folds every shard, and the legacy monolithic catalog, into a
// single shard and removes what it replaced.
//
// Shards accumulate one per flush, so without compaction opening a repository
// costs a request per flush ever made. Callers must hold the repository's
// exclusive lock: this is the one operation that removes index material, and
// doing it alongside a concurrent writer could drop a shard written between the
// merge and the delete.
//
// The consolidated shard is written before anything is deleted. A reader that
// lists midway sees both it and its inputs, which merge to the same result.
func (s *PackStore) CompactCatalog(ctx context.Context) (int, error) {
	if err := s.ensureCatalogLoaded(ctx); err != nil {
		return 0, err
	}

	// Only objects this store has actually absorbed are candidates for removal.
	// Listing the prefix instead would put a shard written by someone else since
	// we loaded into the delete set, and we would remove it without ever having
	// read it.
	//
	// The catalog is sealed here, under the same lock, rather than copied out and
	// marshalled afterwards. Sealing does no I/O, so the lock is held only for the
	// marshal, and a repository large enough for compaction to matter is exactly
	// the one whose catalog is expensive to duplicate.
	s.mu.RLock()
	absorbed := make([]string, 0, len(s.mergedIndex))
	for key := range s.mergedIndex {
		absorbed = append(absorbed, key)
	}
	pendingRemoval := s.needsCompaction
	shouldCompact := len(absorbed) > 1 || pendingRemoval
	var consolidated string
	var sealed []byte
	var err error
	if shouldCompact {
		consolidated, sealed, err = s.sealShardFor(s.catalog, keySetOfCatalog(s.catalog))
	}
	s.mu.RUnlock()

	// Consolidating a single object gains nothing — but a deletion is only
	// durable once the index is rewritten, so a pending removal makes the
	// rewrite necessary rather than merely useful.
	if !shouldCompact {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	// An empty catalog has no consolidated shard to write. The inputs are still
	// removed: every entry they named is gone, which is the deletion this
	// compaction exists to make durable.
	if consolidated != "" {
		if err := s.ObjectStore.Put(ctx, consolidated, sealed); err != nil {
			return 0, fmt.Errorf("write pack index shard %s: %w", consolidated, err)
		}
		s.debugf("pack: wrote consolidated shard %s", consolidated)
	}

	// Only now remove the inputs. Anything that fails to delete merges to the
	// same result, so a later compaction reclaims it.
	removed := 0
	for _, key := range absorbed {
		if key == consolidated {
			continue
		}
		if err := s.ObjectStore.Delete(ctx, key); err != nil {
			return removed, fmt.Errorf("delete superseded index object %s: %w", key, err)
		}
		removed++
	}

	s.mu.Lock()
	s.needsCompaction = false
	s.mergedIndex = make(map[string]bool, 1)
	if consolidated != "" {
		s.mergedIndex[consolidated] = true
	}
	s.mu.Unlock()

	s.debugf("pack: compacted %d index objects into %s", removed, consolidated)
	return removed, nil
}

// keySetOfCatalog names every entry, for a compaction that seals the whole
// catalog rather than one flush's worth of it.
func keySetOfCatalog(c *packCatalog) map[string]struct{} {
	keys := make(map[string]struct{}, c.Len())
	c.Each(func(key string, _ PackEntry) { keys[key] = struct{}{} })
	return keys
}
