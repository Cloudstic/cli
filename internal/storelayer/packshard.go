package storelayer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

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

// packRefInterner keeps the one canonical copy of each pack name while index
// objects are decoded. It is needed only for the duration of a catalog load:
// the catalog entries retain the canonical strings after this map is released.
type packRefInterner map[string]string

func newPackRefInterner(catalog map[string]PackEntry) packRefInterner {
	refs := make(packRefInterner)
	for key, entry := range catalog {
		if ref, ok := refs[entry.PackRef]; ok {
			entry.PackRef = ref
			catalog[key] = entry
			continue
		}
		refs[entry.PackRef] = entry.PackRef
	}
	return refs
}

func (i packRefInterner) intern(ref string) string {
	if existing, ok := i[ref]; ok {
		return existing
	}
	i[ref] = ref
	return ref
}

// mergePackIndex decodes one JSON index object directly into catalog. Decoding
// an entry before deciding whether to insert it keeps duplicate keys harmless,
// while avoiding an intermediate map proportional to the whole shard.
//
// It reports how many entries the object contained, not how many were inserted.
// The two differ whenever a key is already present, and the caller's question —
// did the stored index describe anything — is answered by the former. Counting
// insertions instead would read a healthy index as empty whenever the entries it
// names were already in the catalog, which is what auto-flush leaves behind.
func mergePackIndex(data []byte, catalog map[string]PackEntry, refs packRefInterner) (int, error) {
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
		if _, exists := catalog[key]; exists {
			continue
		}
		entry.PackRef = refs.intern(entry.PackRef)
		catalog[key] = entry
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

// writeShard persists entries as a new immutable shard, named by the hash of
// its own contents. Two runs that happen to produce identical shards write the
// same object, which is harmless.
func (s *PackStore) writeShard(ctx context.Context, entries map[string]PackEntry) error {
	if len(entries) == 0 {
		return nil
	}

	plain, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal pack index shard: %w", err)
	}
	sealed, err := sealIndex(plain, s.indexKey)
	if err != nil {
		return fmt.Errorf("seal pack index shard: %w", err)
	}

	// Name by the plaintext hash so the key is stable regardless of the nonce a
	// sealed shard carries.
	key := shardPrefix + core.ComputeHash(plain)
	if err := s.ObjectStore.Put(ctx, key, sealed); err != nil {
		return fmt.Errorf("write pack index shard %s: %w", key, err)
	}
	s.debugf("pack: wrote shard %s with %d entries", key, len(entries))
	return nil
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
func (s *PackStore) loadShardsLocked(ctx context.Context, refs packRefInterner) (int, error) {
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

		decoded, err := mergePackIndex(plain, s.catalog, refs)
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
	s.mu.RLock()
	absorbed := make([]string, 0, len(s.mergedIndex))
	for key := range s.mergedIndex {
		absorbed = append(absorbed, key)
	}
	pendingRemoval := s.needsCompaction
	merged := make(map[string]PackEntry, len(s.catalog))
	for k, v := range s.catalog {
		merged[k] = v
	}
	s.mu.RUnlock()

	// Consolidating a single object gains nothing — but a deletion is only
	// durable once the index is rewritten, so a pending removal makes the
	// rewrite necessary rather than merely useful.
	if len(absorbed) <= 1 && !pendingRemoval {
		return 0, nil
	}

	if err := s.writeShard(ctx, merged); err != nil {
		return 0, err
	}
	consolidated := shardPrefix + core.ComputeHash(mustMarshal(merged))

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
	s.mergedIndex = map[string]bool{consolidated: true}
	s.mu.Unlock()

	s.debugf("pack: compacted %d index objects into %s", removed, consolidated)
	return removed, nil
}

// mustMarshal reproduces the shard key for an already-validated map. The map
// came from writeShard, which marshalled it successfully a moment earlier.
func mustMarshal(entries map[string]PackEntry) []byte {
	data, err := json.Marshal(entries)
	if err != nil {
		return nil
	}
	return data
}
