package storelayer

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// legacyPackStore writes packs the way a pre-footer client did: a bare
// concatenation of object bytes with no trailer.
func writeLegacyPack(t *testing.T, inner store.ObjectStore, objects map[string][]byte) string {
	t.Helper()
	ctx := context.Background()

	var buf []byte
	entries := make(map[string]PackEntry, len(objects))
	for key, data := range objects {
		entries[key] = PackEntry{Offset: int64(len(buf)), Length: int64(len(data))}
		buf = append(buf, data...)
	}

	packRef := packPrefix + "legacy0000000000000000000000000000000000000000000000000000000000"
	if err := inner.Put(ctx, packRef, buf); err != nil {
		t.Fatalf("write legacy pack: %v", err)
	}

	// The monolithic catalog is the only record of these offsets, and it is
	// written directly: current code writes shards, so routing through Flush
	// would not reproduce a pre-shard repository.
	for key, entry := range entries {
		entry.PackRef = packRef
		entries[key] = entry
	}
	catalog, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := inner.Put(ctx, indexPacksKey, catalog); err != nil {
		t.Fatalf("write legacy catalog: %v", err)
	}
	return packRef
}

func TestPackFooter_RoundTrip(t *testing.T) {
	entries := map[string]PackEntry{
		"filemeta/a": {Offset: 0, Length: 10},
		"node/b":     {Offset: 10, Length: 25},
	}
	footer, err := buildPackFooter(entries, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	pack := append(make([]byte, 35), footer...)
	info, err := decodePackFooter("packs/test", pack, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.objectRegionLen != 35 {
		t.Errorf("object region = %d, want 35", info.objectRegionLen)
	}
	for key, want := range entries {
		got, ok := info.entries[key]
		if !ok {
			t.Errorf("missing %s", key)
			continue
		}
		if got.Offset != want.Offset || got.Length != want.Length {
			t.Errorf("%s = %+v, want offset %d length %d", key, got, want.Offset, want.Length)
		}
		if got.PackRef != "packs/test" {
			t.Errorf("%s PackRef = %q", key, got.PackRef)
		}
	}
}

// Identical object sets must produce byte-identical footers, or the
// content-addressed packfile name stops being reproducible.
func TestPackFooter_IsDeterministic(t *testing.T) {
	entries := map[string]PackEntry{
		"node/z": {Offset: 20, Length: 5},
		"node/a": {Offset: 0, Length: 20},
		"node/m": {Offset: 25, Length: 7},
	}
	first, err := buildPackFooter(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := buildPackFooter(entries, nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("footer is not deterministic across map iteration orders")
		}
	}
}

func TestPackFooter_RejectsCorruptFooter(t *testing.T) {
	entries := map[string]PackEntry{"filemeta/a": {Offset: 0, Length: 10}}
	footer, err := buildPackFooter(entries, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("no magic", func(t *testing.T) {
		if _, err := decodePackFooter("packs/x", []byte("just some object bytes"), nil); !errors.Is(err, errNoPackFooter) {
			t.Errorf("want errNoPackFooter, got %v", err)
		}
	})

	t.Run("too short", func(t *testing.T) {
		if _, err := decodePackFooter("packs/x", []byte{1, 2}, nil); !errors.Is(err, errNoPackFooter) {
			t.Errorf("want errNoPackFooter, got %v", err)
		}
	})

	t.Run("offset outside object region", func(t *testing.T) {
		bad, err := buildPackFooter(map[string]PackEntry{"filemeta/a": {Offset: 0, Length: 999}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		pack := append(make([]byte, 10), bad...)
		if _, err := decodePackFooter("packs/x", pack, nil); err == nil {
			t.Error("want an error for an entry outside the object region")
		}
	})

	t.Run("implausible declared length", func(t *testing.T) {
		// A corrupt or hostile trailer must not be able to drive an
		// arbitrarily large read and allocation.
		trailer := make([]byte, packTrailerLen)
		binary.BigEndian.PutUint32(trailer[:4], ^uint32(0))
		trailer[4] = packFooterVersion
		copy(trailer[5:], packMagic)

		if _, err := parsePackTrailer(trailer); err == nil {
			t.Error("want an error for a footer length above the limit")
		} else if !strings.Contains(err.Error(), "limit") {
			t.Errorf("error should name the limit, got: %v", err)
		}
	})

	t.Run("truncated payload", func(t *testing.T) {
		pack := append(make([]byte, 10), footer...)
		// Keep the trailer but destroy the payload it points at.
		trimmed := append([]byte{}, pack[:5]...)
		trimmed = append(trimmed, pack[len(pack)-packTrailerLen:]...)
		if _, err := decodePackFooter("packs/x", trimmed, nil); err == nil {
			t.Error("want an error for a truncated footer payload")
		}
	})
}

// Every pack written today carries a footer describing exactly what is in it.
func TestPackStore_WritesFooterMatchingCatalog(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)

	ps := newPackStoreT(t, inner)
	keys := []string{"filemeta/a", "node/b", "snapshot/c"}
	for _, key := range keys {
		if err := ps.Put(ctx, key, []byte("payload for "+key)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	packs, err := inner.List(ctx, packPrefix)
	if err != nil || len(packs) != 1 {
		t.Fatalf("expected 1 pack, got %v (err %v)", packs, err)
	}

	info, err := ps.readPackFooter(ctx, packs[0])
	if err != nil {
		t.Fatalf("read footer: %v", err)
	}
	for _, key := range keys {
		footerEntry, ok := info.entries[key]
		if !ok {
			t.Errorf("footer missing %s", key)
			continue
		}
		ps.mu.RLock()
		catalogEntry := mustCatalogGet(t, ps.catalog, key)
		ps.mu.RUnlock()
		if footerEntry != catalogEntry {
			t.Errorf("%s: footer %+v != catalog %+v", key, footerEntry, catalogEntry)
		}
	}
}

// The headline property: losing index/packs entirely is survivable.
func TestPackStore_SelfHealsFromFootersWhenCatalogIsLost(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)

	seedPackedRepo(t, inner, "filemeta/a", "node/b", "snapshot/c")

	// Simulate total loss of the index.
	dropIndexObjects(t, inner)

	recovered := newPackStoreT(t, inner)
	for _, key := range []string{"filemeta/a", "node/b", "snapshot/c"} {
		data, err := recovered.Get(ctx, key)
		if err != nil {
			t.Fatalf("get %s after catalog loss: %v", key, err)
		}
		if string(data) != "payload for "+key {
			t.Errorf("%s = %q, want %q", key, data, "payload for "+key)
		}
	}

	keys, err := recovered.List(ctx, "snapshot/")
	if err != nil {
		t.Fatalf("list after catalog loss: %v", err)
	}
	if len(keys) != 1 || keys[0] != "snapshot/c" {
		t.Errorf("List = %v, want [snapshot/c]", keys)
	}

	// The healed catalog is written back, so the next session pays nothing.
	if err := recovered.Flush(ctx); err != nil {
		t.Fatalf("flush healed catalog: %v", err)
	}
	if shards, err := inner.List(ctx, shardPrefix); err != nil || len(shards) == 0 {
		t.Errorf("healed index should be persisted as a shard (shards=%v, err=%v)", shards, err)
	}
}

// RebuildCatalog is the explicit repair entry point, and must be idempotent.
func TestPackStore_RebuildCatalogIsIdempotent(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)
	seedPackedRepo(t, inner, "filemeta/a", "node/b")

	ps := newPackStoreT(t, inner)
	recovered, footerless, err := ps.RebuildCatalog(ctx)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if recovered != 2 || footerless != 0 {
		t.Errorf("first rebuild: recovered %d, footerless %d; want 2, 0", recovered, footerless)
	}

	// Second pass finds everything already present.
	recovered, footerless, err = ps.RebuildCatalog(ctx)
	if err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if recovered != 0 || footerless != 0 {
		t.Errorf("second rebuild: recovered %d, footerless %d; want 0, 0", recovered, footerless)
	}
}

// Backward compatibility: a repository written by a pre-footer client still
// reads correctly through the legacy catalog.
func TestPackStore_ReadsLegacyFooterlessPacks(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)

	writeLegacyPack(t, inner, map[string][]byte{
		"filemeta/legacy1": []byte("first legacy object"),
		"filemeta/legacy2": []byte("second legacy object"),
	})

	ps := newPackStoreT(t, inner)
	data, err := ps.Get(ctx, "filemeta/legacy1")
	if err != nil {
		t.Fatalf("read legacy packed object: %v", err)
	}
	if string(data) != "first legacy object" {
		t.Errorf("got %q", data)
	}

	keys, err := ps.List(ctx, "filemeta/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 legacy keys, got %v", keys)
	}
}

// A mixed repository — some packs footered, some not — must work, and new
// writes must not disturb the legacy entries.
func TestPackStore_MixedLegacyAndFooteredPacks(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)

	writeLegacyPack(t, inner, map[string][]byte{"filemeta/legacy1": []byte("legacy payload")})

	ps := newPackStoreT(t, inner)
	if err := ps.Put(ctx, "filemeta/modern", []byte("modern payload")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	fresh := newPackStoreT(t, inner)
	for key, want := range map[string]string{
		"filemeta/legacy1": "legacy payload",
		"filemeta/modern":  "modern payload",
	} {
		data, err := fresh.Get(ctx, key)
		if err != nil {
			t.Errorf("get %s: %v", key, err)
			continue
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", key, data, want)
		}
	}

	// A rebuild recovers the modern pack and reports the legacy one as
	// unrecoverable, rather than silently dropping it.
	rebuilt := newPackStoreT(t, inner)
	recovered, footerless, err := rebuilt.RebuildCatalog(ctx)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if recovered != 1 {
		t.Errorf("recovered %d entries, want 1", recovered)
	}
	if footerless != 1 {
		t.Errorf("footerless = %d, want 1", footerless)
	}
}

// Losing the catalog on a repository with pre-footer packs cannot be healed.
// That must be reported plainly rather than presented as a successful recovery.
func TestPackStore_ReportsUnhealableLegacyPacks(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)

	writeLegacyPack(t, inner, map[string][]byte{"filemeta/legacy1": []byte("legacy payload")})
	dropIndexObjects(t, inner)

	ps := newPackStoreT(t, inner)
	_, err := ps.List(ctx, "filemeta/")
	if err == nil {
		t.Fatal("want an error when a footerless pack's catalog is lost")
	}
	if !strings.Contains(err.Error(), "predate self-describing footers") {
		t.Errorf("error should explain why recovery is impossible, got: %v", err)
	}
}

// Forward compatibility: an older client addresses objects by explicit offset
// and length, so trailing footer bytes are inert to it.
func TestPackStore_FooterIsInertToOffsetBasedReaders(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)

	ps := newPackStoreT(t, inner)
	if err := ps.Put(ctx, "filemeta/a", []byte("exact payload")); err != nil {
		t.Fatal(err)
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	ps.mu.RLock()
	entry := mustCatalogGet(t, ps.catalog, "filemeta/a")
	ps.mu.RUnlock()

	// Read the way a pre-footer client does: raw pack bytes, sliced by the
	// catalog's offset and length, with no awareness of a trailer.
	raw, err := inner.Get(ctx, entry.PackRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw[entry.Offset : entry.Offset+entry.Length]); got != "exact payload" {
		t.Errorf("offset-based read got %q, want %q", got, "exact payload")
	}
}

// GetRange must agree with a full Get, since footer recovery depends on it.
func TestLocalStore_GetRangeMatchesFullGet(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	ls, err := local.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	if err := ls.Put(ctx, "chunk/x", payload); err != nil {
		t.Fatal(err)
	}

	var ranger store.RangeGetter = ls
	for _, tc := range []struct{ offset, length int64 }{{0, 5}, {10, 6}, {30, 6}, {0, 36}} {
		got, err := ranger.GetRange(ctx, "chunk/x", tc.offset, tc.length)
		if err != nil {
			t.Fatalf("GetRange(%d,%d): %v", tc.offset, tc.length, err)
		}
		if want := string(payload[tc.offset : tc.offset+tc.length]); string(got) != want {
			t.Errorf("GetRange(%d,%d) = %q, want %q", tc.offset, tc.length, got, want)
		}
	}

	if _, err := ranger.GetRange(ctx, "chunk/x", 30, 100); err == nil {
		t.Error("reading past the end should fail")
	}
}
