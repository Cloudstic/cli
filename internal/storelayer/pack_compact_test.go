package storelayer

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store/storetest"
)

// writeShard names the object it wrote. Compaction used to derive that name by
// marshalling the same map a second time, which both doubled the cost and left
// room for the two to disagree.
func TestWriteShard_ReturnsTheKeyItWrote(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()

	s, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}

	key, err := s.writeShard(ctx, map[string]PackEntry{
		"filemeta/a": {PackRef: "packs/one", Offset: 0, Length: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, shardPrefix) {
		t.Fatalf("key %q is not under %q", key, shardPrefix)
	}
	if _, err := mem.Get(ctx, key); err != nil {
		t.Fatalf("nothing was written at the returned key %s: %v", key, err)
	}

	// Nothing to write is not an error, and names no object.
	empty, err := s.writeShard(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty != "" {
		t.Fatalf("empty shard returned key %q, want \"\"", empty)
	}
}

// Compacting a catalog whose entries have all been deleted has no consolidated
// shard to write, but must still remove the shards that named them -- that
// removal is the whole reason the compaction runs. It must not claim a
// consolidated shard exists, either, or the next load merges an absent object.
func TestCompactCatalog_EmptiedCatalogRemovesItsShards(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()

	s, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"filemeta/a", "filemeta/b"}
	for _, k := range keys {
		if err := s.Put(ctx, k, []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err := s.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.CompactCatalog(ctx); err != nil {
		t.Fatalf("CompactCatalog: %v", err)
	}

	s.mu.RLock()
	merged := make([]string, 0, len(s.mergedIndex))
	for k := range s.mergedIndex {
		merged = append(merged, k)
	}
	s.mu.RUnlock()

	for _, k := range merged {
		if k == "" {
			t.Error("mergedIndex records an empty key")
			continue
		}
		if _, err := mem.Get(ctx, k); err != nil {
			t.Errorf("mergedIndex names %s, which does not exist: %v", k, err)
		}
	}

	// A fresh reader must not resurrect the deleted entries.
	fresh, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if ok, err := fresh.Exists(ctx, k); err != nil {
			t.Errorf("Exists(%s): %v", k, err)
		} else if ok {
			t.Errorf("%s came back after compaction removed it", k)
		}
	}
}

// The consolidated shard a compaction writes is the one it then refuses to
// delete. If the key it recorded and the key it wrote could differ, compaction
// would delete its own output and leave the catalog with nothing on the store.
func TestCompactCatalog_KeepsTheShardItWrote(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()

	writer, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	// Two flushes, so there are two shards to consolidate.
	for _, k := range []string{"filemeta/a", "filemeta/b"} {
		if err := writer.Put(ctx, k, []byte("v")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// Compaction consolidates only shards the store has read. A store that wrote
	// them never absorbed them, so the compaction has to run on a fresh reader.
	s, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompactCatalog(ctx); err != nil {
		t.Fatalf("CompactCatalog: %v", err)
	}

	shards, err := mem.List(ctx, shardPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(shards) != 1 {
		t.Fatalf("after compaction there are %d shards, want 1: %v", len(shards), shards)
	}

	fresh, err := NewPackStore(mem)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"filemeta/a", "filemeta/b"} {
		got, err := fresh.Get(ctx, k)
		if err != nil {
			t.Errorf("Get(%s) after compaction: %v", k, err)
		} else if string(got) != "v" {
			t.Errorf("Get(%s) = %q, want %q", k, got, "v")
		}
	}
}
