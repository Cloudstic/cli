package storelayer

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
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

// listSpyStore records the prefixes listed through it. Whether the heal ran is
// otherwise invisible from outside: it is the only thing in a plain Get that
// lists packPrefix, and on a repository whose packs are intact it would repair
// nothing and return no error, so its effect cannot be observed in the result.
type listSpyStore struct {
	store.ObjectStore
	mu       sync.Mutex
	prefixes []string
}

func (s *listSpyStore) List(ctx context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	s.prefixes = append(s.prefixes, prefix)
	s.mu.Unlock()
	return s.ObjectStore.List(ctx, prefix)
}

func (s *listSpyStore) listed(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.prefixes {
		if p == prefix {
			return true
		}
	}
	return false
}

// The case the entry count exists to distinguish from an empty index: a healthy
// index whose keys are all already in the catalog because Put's auto-flush put
// them there. Counting insertions rather than decoded entries would read this as
// an empty index and heal a repository that never lost anything.
func TestPackStore_DoesNotHealWhenIndexEntriesAreAlreadyKnown(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()

	key, payload := seedPackedObject(t, ctx, mem)
	entry := shardEntryFor(t, ctx, mem, key)

	spy := &listSpyStore{ObjectStore: mem}
	fresh, err := NewPackStore(spy)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-populate the catalog the way an auto-flush would, so every entry the
	// stored shard names is already present and none of them is inserted.
	fresh.mu.Lock()
	fresh.catalog.Set(key, entry)
	fresh.pendingKeys[key] = struct{}{}
	fresh.mu.Unlock()

	// Read a key the catalog does not hold, so the load actually runs rather
	// than being short-circuited by the seeded entry.
	if _, err := fresh.Get(ctx, "filemeta/"+core.ComputeHash([]byte("absent"))); err == nil {
		t.Fatal("expected a miss for an object that was never stored")
	}

	if spy.listed(packPrefix) {
		t.Errorf("the heal ran on a repository whose index was intact: listed %q", packPrefix)
	}

	fresh.mu.RLock()
	loaded := fresh.catalogLoaded
	fresh.mu.RUnlock()
	if !loaded {
		t.Error("catalog did not finish loading")
	}

	got, err := fresh.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get on the indexed key failed: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

// shardEntryFor reads the stored shards and returns the entry recorded for key,
// so a test can seed the catalog with exactly what the index already says.
//
// It fails the test directly rather than returning an error, matching the other
// helpers here: every caller could only pass the error to t.Fatal, and naming
// the shard at the point of failure is what makes a broken fixture diagnosable.
func shardEntryFor(t *testing.T, ctx context.Context, mem *storetest.MemStore, key string) PackEntry {
	t.Helper()

	keys, err := mem.List(ctx, shardPrefix)
	if err != nil {
		t.Fatalf("list pack index shards under %s: %v", shardPrefix, err)
	}
	catalog := newPackCatalog()
	for _, k := range keys {
		data, err := mem.Get(ctx, k)
		if err != nil {
			t.Fatalf("read pack index shard %s: %v", k, err)
		}
		if _, err := mergePackIndex(data, catalog); err != nil {
			t.Fatalf("merge pack index shard %s: %v", k, err)
		}
	}

	entry, ok := catalog.Get(key)
	if !ok {
		t.Fatalf("no shard records an entry for %s; the fixture never indexed it", key)
	}
	return entry
}

// mergePackIndex reports what the object described, not what it inserted. The
// difference is the whole basis of the heal decision.
func TestMergePackIndex_CountsDecodedEntriesNotInsertions(t *testing.T) {
	shard := []byte(`{"filemeta/a":{"p":"packs/one","o":0,"l":1},"filemeta/b":{"p":"packs/one","o":1,"l":1}}`)

	catalog := newPackCatalog()
	decoded, err := mergePackIndex(shard, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != 2 {
		t.Fatalf("decoded = %d, want 2", decoded)
	}

	// Same object into a catalog that already holds both keys: nothing is
	// inserted, but the object still described two entries.
	again, err := mergePackIndex(shard, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if again != 2 {
		t.Fatalf("decoded on re-merge = %d, want 2 (insertions would be 0)", again)
	}

	empty := newPackCatalog()
	if n, err := mergePackIndex([]byte("{}"), empty); err != nil || n != 0 {
		t.Fatalf("empty object: got (%d, %v), want (0, nil)", n, err)
	}
}
