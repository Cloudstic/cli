package storelayer

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"unsafe"

	"github.com/cloudstic/cli/pkg/store/local"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

func TestPackStore_LoadShardsInternsPackRefs(t *testing.T) {
	ctx := context.Background()
	base := storetest.NewMemStore()

	writer, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.writeShard(ctx, map[string]PackEntry{
		"filemeta/a": {PackRef: "packs/shared", Offset: 0, Length: 1},
		"filemeta/b": {PackRef: "packs/shared", Offset: 1, Length: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.writeShard(ctx, map[string]PackEntry{
		"node/c": {PackRef: "packs/shared", Offset: 2, Length: 1},
		"node/d": {PackRef: "packs/other", Offset: 0, Length: 1},
	}); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.ensureCatalogLoaded(ctx); err != nil {
		t.Fatal(err)
	}

	a := reader.catalog["filemeta/a"].PackRef
	b := reader.catalog["filemeta/b"].PackRef
	c := reader.catalog["node/c"].PackRef
	if unsafe.StringData(a) != unsafe.StringData(b) || unsafe.StringData(a) != unsafe.StringData(c) {
		t.Fatal("entries sharing a pack ref retained separate string allocations")
	}
	if unsafe.StringData(a) == unsafe.StringData(reader.catalog["node/d"].PackRef) {
		t.Fatal("distinct pack refs unexpectedly share storage")
	}
}

func TestMergePackIndex_IsFirstWriterWinsOrderIndependentAndIdempotent(t *testing.T) {
	first := []byte(`{
		"filemeta/a":{"p":"packs/one","o":0,"l":1},
		"filemeta/shared":{"p":"packs/one","o":1,"l":1}
	}`)
	second := []byte(`{
		"node/b":{"p":"packs/two","o":0,"l":1},
		"filemeta/shared":{"p":"packs/one","o":1,"l":1}
	}`)

	merge := func(parts ...[]byte) map[string]PackEntry {
		t.Helper()
		catalog := make(map[string]PackEntry)
		refs := newPackRefInterner(catalog)
		for _, part := range parts {
			if _, err := mergePackIndex(part, catalog, refs); err != nil {
				t.Fatal(err)
			}
		}
		return catalog
	}

	forward := merge(first, second)
	reverse := merge(second, first)
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("merge depends on shard order:\nforward: %#v\nreverse: %#v", forward, reverse)
	}
	if twice := merge(first, second, first, second); !reflect.DeepEqual(forward, twice) {
		t.Fatalf("merge is not idempotent:\nonce: %#v\ntwice: %#v", forward, twice)
	}

	winner := PackEntry{PackRef: "packs/winner", Offset: 7, Length: 9}
	catalog := map[string]PackEntry{"filemeta/shared": winner}
	if _, err := mergePackIndex(first, catalog, newPackRefInterner(catalog)); err != nil {
		t.Fatal(err)
	}
	if got := catalog["filemeta/shared"]; got != winner {
		t.Fatalf("later shard replaced the first entry: got %#v, want %#v", got, winner)
	}
}

func TestMergePackIndex_RejectsMalformedInput(t *testing.T) {
	tests := map[string]string{
		"empty":          "",
		"not an object":  `[]`,
		"truncated":      `{"filemeta/a":{"p":"packs/one","o":0,"l":1}`,
		"bad entry type": `{"filemeta/a":{"p":[],"o":0,"l":1}}`,
		"trailing value": `{} {}`,
		"trailing junk":  `{} x`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			catalog := make(map[string]PackEntry)
			if _, err := mergePackIndex([]byte(input), catalog, newPackRefInterner(catalog)); err == nil {
				t.Fatal("merge succeeded for malformed input")
			}
		})
	}
}

// The race the shard layout exists to remove: two writers sharing a repository.
//
// With a single mutable catalog both loaded the same object, appended their own
// entries, and wrote it back — last writer wins, and the loser's entries became
// unaddressable. Backups take shared locks by design, so this was reachable in
// normal use, and unlike index/snapshots it could not be reconciled afterwards
// because pack offsets exist nowhere else.
func TestPackStore_ConcurrentWritersDoNotEraseEachOther(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const writers = 4
	const perWriter = 3

	var wg sync.WaitGroup
	errs := make(chan error, writers)

	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// A separate store per writer, as separate processes would have.
			ps, err := NewPackStore(base)
			if err != nil {
				errs <- err
				return
			}
			for i := range perWriter {
				key := fmt.Sprintf("filemeta/w%d-%d", w, i)
				if err := ps.Put(ctx, key, []byte(key+" payload")); err != nil {
					errs <- err
					return
				}
			}
			if err := ps.Flush(ctx); err != nil {
				errs <- err
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("writer failed: %v", err)
		}
	}

	// Every writer's objects must survive, not just the last one to flush.
	reader, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	for w := range writers {
		for i := range perWriter {
			key := fmt.Sprintf("filemeta/w%d-%d", w, i)
			data, err := reader.Get(ctx, key)
			if err != nil {
				t.Errorf("%s was lost: %v", key, err)
				continue
			}
			if string(data) != key+" payload" {
				t.Errorf("%s = %q", key, data)
			}
		}
	}
}

// Compaction folds shards into one. A reader that lists midway sees the
// consolidated shard alongside its inputs, which must merge to the same result.
func TestPackStore_CompactionIsTransparentToReaders(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Several flushes, so several shards.
	keys := []string{"filemeta/a", "filemeta/b", "node/c", "snapshot/d"}
	for _, key := range keys {
		ps, err := NewPackStore(base)
		if err != nil {
			t.Fatal(err)
		}
		if err := ps.Put(ctx, key, []byte("payload for "+key)); err != nil {
			t.Fatal(err)
		}
		if err := ps.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}

	before, err := base.List(ctx, shardPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) < 2 {
		t.Fatalf("expected several shards to compact, got %d", len(before))
	}

	compactor, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := compactor.CompactCatalog(ctx)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if removed == 0 {
		t.Error("compaction consolidated nothing")
	}

	after, err := base.List(ctx, shardPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Errorf("expected a single shard after compaction, got %d", len(after))
	}

	// Everything still resolves, from a store that has seen only the result.
	reader, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		data, err := reader.Get(ctx, key)
		if err != nil {
			t.Errorf("get %s after compaction: %v", key, err)
			continue
		}
		if string(data) != "payload for "+key {
			t.Errorf("%s = %q", key, data)
		}
	}
}

// Compaction is also what makes a deletion durable: shards are append-only, so
// removing an entry cannot be expressed by writing another one.
func TestPackStore_CompactionPersistsDeletions(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	writer, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"filemeta/keep", "filemeta/drop"} {
		if err := writer.Put(ctx, key, []byte("payload for "+key)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	pruner, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := pruner.Delete(ctx, "filemeta/drop"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := pruner.CompactCatalog(ctx); err != nil {
		t.Fatalf("compact: %v", err)
	}

	reader, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Get(ctx, "filemeta/keep"); err != nil {
		t.Errorf("the surviving entry was lost: %v", err)
	}
	keys, err := reader.List(ctx, "filemeta/")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if key == "filemeta/drop" {
			t.Error("the deleted entry came back after compaction")
		}
	}
}

// A repository holding both a pre-shard monolithic catalog and shards must read
// as the union of the two, which is the state every upgraded repository is in
// until its first compaction.
func TestPackStore_ReadsLegacyCatalogAlongsideShards(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	writeLegacyPack(t, base, map[string][]byte{"filemeta/legacy": []byte("legacy payload")})

	modern, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := modern.Put(ctx, "filemeta/modern", []byte("modern payload")); err != nil {
		t.Fatal(err)
	}
	if err := modern.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"filemeta/legacy": "legacy payload",
		"filemeta/modern": "modern payload",
	} {
		data, err := reader.Get(ctx, key)
		if err != nil {
			t.Errorf("get %s: %v", key, err)
			continue
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", key, data, want)
		}
	}

	// Compaction folds the legacy object in and removes it.
	if _, err := reader.CompactCatalog(ctx); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if exists, _ := base.Exists(ctx, indexPacksKey); exists {
		t.Error("compaction should have removed the legacy catalog")
	}

	final, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := final.Get(ctx, "filemeta/legacy"); err != nil {
		t.Errorf("legacy entry lost after compaction: %v", err)
	}
}
