package store

import (
	"context"
	"encoding/json"
	"fmt"

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
	debugf("pack: wrote shard %s with %d entries", key, len(entries))
	return nil
}

// loadShardsLocked merges every shard into the in-memory catalog and returns
// how many were read. mu must be held.
//
// A shard that cannot be read fails the caller. Continuing with a partial merge
// would produce a catalog that looks complete and is not, which is how a prune
// deletes objects it simply could not see.
func (s *PackStore) loadShardsLocked(ctx context.Context) (int, error) {
	keys, err := s.ObjectStore.List(ctx, shardPrefix)
	if err != nil {
		return 0, fmt.Errorf("list pack index shards: %w", err)
	}

	for _, key := range keys {
		data, err := s.ObjectStore.Get(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("read pack index shard %s: %w", key, err)
		}
		plain, err := openIndex(data, s.indexKey)
		if err != nil {
			return 0, fmt.Errorf("open pack index shard %s: %w", key, err)
		}

		var entries map[string]PackEntry
		if err := json.Unmarshal(plain, &entries); err != nil {
			return 0, fmt.Errorf("unmarshal pack index shard %s: %w", key, err)
		}
		for k, entry := range entries {
			if _, exists := s.catalog[k]; !exists {
				s.catalog[k] = entry
			}
		}
		s.mergedIndex[key] = true
	}

	if len(keys) > 0 {
		debugf("pack: merged %d shards", len(keys))
	}
	return len(keys), nil
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

	debugf("pack: compacted %d index objects into %s", removed, consolidated)
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
