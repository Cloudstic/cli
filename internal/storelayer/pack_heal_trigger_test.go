package storelayer

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

// seedPackedObject writes one small object through a PackStore and flushes it,
// leaving a real packfile with a footer on mem. It returns the object's key and
// its bytes.
func seedPackedObject(t *testing.T, ctx context.Context, mem *storetest.MemStore) (string, []byte) {
	t.Helper()

	seed, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	key := "filemeta/" + core.ComputeHash([]byte("recoverable"))
	payload := []byte("payload-bytes")
	if err := seed.Put(ctx, key, payload); err != nil {
		t.Fatal(err)
	}
	if err := seed.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	return key, payload
}

// dropShards removes every shard the seeding flush wrote, simulating a lost
// index while the packs themselves survive.
func dropShards(t *testing.T, ctx context.Context, mem *storetest.MemStore) {
	t.Helper()

	shards, err := mem.List(ctx, shardPrefix)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range shards {
		if err := mem.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
}

// An index object that exists and lists nothing leaves the repository exactly as
// unindexed as a missing one. Keying the heal on whether index/packs exists read
// the empty object as authoritative, so a packed object came back as not found
// even though its pack — and its footer — were intact.
func TestPackStore_HealsWhenLegacyCatalogIsEmpty(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()

	key, payload := seedPackedObject(t, ctx, mem)
	dropShards(t, ctx, mem)
	if err := mem.Put(ctx, indexPacksKey, []byte("{}")); err != nil {
		t.Fatal(err)
	}

	fresh, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fresh.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed instead of healing from the pack footer: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

// The same hole, reached through a shard rather than the legacy catalog.
func TestPackStore_HealsWhenShardsDescribeNothing(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()

	key, payload := seedPackedObject(t, ctx, mem)
	dropShards(t, ctx, mem)
	if err := mem.Put(ctx, shardPrefix+"empty", []byte("{}")); err != nil {
		t.Fatal(err)
	}

	fresh, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fresh.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed instead of healing from the pack footer: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

// The case the entry count exists to distinguish from an empty index: a healthy
// index whose keys are all already in the catalog because Put's auto-flush put
// them there. Counting insertions rather than decoded entries would read this as
// an empty index and heal a repository that never lost anything.
func TestPackStore_DoesNotHealWhenIndexEntriesAreAlreadyKnown(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()

	key, payload := seedPackedObject(t, ctx, mem)

	// Strip the footers so a heal, if one were triggered, could not quietly
	// succeed and hide the misfire. A footerless pack makes healMissingCatalog
	// fail loudly instead.
	packs, err := mem.List(ctx, packPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) == 0 {
		t.Fatal("expected at least one pack")
	}
	for _, ref := range packs {
		data, err := mem.Get(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := mem.Put(ctx, ref, data[:len(data)/2]); err != nil {
			t.Fatal(err)
		}
	}

	fresh, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-populate the catalog the way an auto-flush would, so every entry the
	// stored shard names is already present and none of them is inserted.
	entry, err := shardEntryFor(ctx, mem, key)
	if err != nil {
		t.Fatal(err)
	}
	fresh.mu.Lock()
	fresh.catalog[key] = entry
	fresh.pendingShard[key] = entry
	fresh.mu.Unlock()

	if _, err := fresh.Get(ctx, key); err != nil {
		t.Fatalf("Get healed (and failed) on a repository whose index was intact: %v", err)
	}
	_ = payload
}

// shardEntryFor reads the stored shards and returns the entry recorded for key,
// so a test can seed the catalog with exactly what the index already says.
func shardEntryFor(ctx context.Context, mem *storetest.MemStore, key string) (PackEntry, error) {
	keys, err := mem.List(ctx, shardPrefix)
	if err != nil {
		return PackEntry{}, err
	}
	catalog := make(map[string]PackEntry)
	for _, k := range keys {
		data, err := mem.Get(ctx, k)
		if err != nil {
			return PackEntry{}, err
		}
		if _, err := mergePackIndex(data, catalog, newPackRefInterner(catalog)); err != nil {
			return PackEntry{}, err
		}
	}
	return catalog[key], nil
}

// mergePackIndex reports what the object described, not what it inserted. The
// difference is the whole basis of the heal decision.
func TestMergePackIndex_CountsDecodedEntriesNotInsertions(t *testing.T) {
	shard := []byte(`{"filemeta/a":{"p":"packs/one","o":0,"l":1},"filemeta/b":{"p":"packs/one","o":1,"l":1}}`)

	catalog := make(map[string]PackEntry)
	decoded, err := mergePackIndex(shard, catalog, newPackRefInterner(catalog))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != 2 {
		t.Fatalf("decoded = %d, want 2", decoded)
	}

	// Same object into a catalog that already holds both keys: nothing is
	// inserted, but the object still described two entries.
	again, err := mergePackIndex(shard, catalog, newPackRefInterner(catalog))
	if err != nil {
		t.Fatal(err)
	}
	if again != 2 {
		t.Fatalf("decoded on re-merge = %d, want 2 (insertions would be 0)", again)
	}

	empty := make(map[string]PackEntry)
	if n, err := mergePackIndex([]byte("{}"), empty, newPackRefInterner(empty)); err != nil || n != 0 {
		t.Fatalf("empty object: got (%d, %v), want (0, nil)", n, err)
	}
}
