package storelayer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/objkey"
)

func TestPackCatalog_RoundTripsCompactKeys(t *testing.T) {
	c := newPackCatalog()
	for _, prefix := range objkey.Namespaces() {
		key := prefix + fmt.Sprintf("%064x", 42)
		want := PackEntry{PackRef: "packs/" + fmt.Sprintf("%064x", 7), Offset: 1234, Length: 5678}
		c.Set(key, want)

		got, ok := c.Get(key)
		if !ok {
			t.Fatalf("%s: not found after Set", key)
		}
		if got != want {
			t.Fatalf("%s: got %#v, want %#v", key, got, want)
		}
		if !c.Has(key) {
			t.Errorf("%s: Has said false", key)
		}
	}
	if len(c.fallback) != 0 {
		t.Errorf("compact keys landed in the fallback map: %v", c.fallback)
	}
}

// Keys that are not "<known prefix>/<64 hex>" must still work. A legacy
// repository packed index/latest before the packable-prefix restriction existed.
func TestPackCatalog_KeepsUnshapedKeysExact(t *testing.T) {
	c := newPackCatalog()
	for _, key := range []string{
		"index/latest",
		"filemeta/short",
		"filemeta/" + fmt.Sprintf("%064s", "zz"), // 64 chars, not hex
		"unknown/" + fmt.Sprintf("%064x", 1),
	} {
		want := PackEntry{PackRef: "packs/x", Offset: 9, Length: 3}
		c.Set(key, want)
		got, ok := c.Get(key)
		if !ok || got != want {
			t.Errorf("%s: got (%#v, %v), want (%#v, true)", key, got, ok, want)
		}
	}
	if len(c.compact) != 0 {
		t.Errorf("unshaped keys were encoded compactly: %v", c.compact)
	}
}

// An offset or length beyond uint32 cannot come from a pack this code writes,
// but the catalog is read from a store. Such an entry must be returned exactly,
// never truncated into a location pointing somewhere else.
func TestPackCatalog_DoesNotTruncateOutOfRangeLocations(t *testing.T) {
	c := newPackCatalog()
	key := "chunk/" + fmt.Sprintf("%064x", 3)
	want := PackEntry{PackRef: "packs/y", Offset: 1 << 40, Length: 12}
	c.Set(key, want)

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("not found")
	}
	if got != want {
		t.Fatalf("got %#v, want %#v — an out-of-range offset was not preserved", got, want)
	}
}

func TestPackCatalog_DeleteAndLenAndEach(t *testing.T) {
	c := newPackCatalog()
	compactKeyName := "node/" + fmt.Sprintf("%064x", 1)
	c.Set(compactKeyName, PackEntry{PackRef: "packs/a", Length: 1})
	c.Set("index/latest", PackEntry{PackRef: "packs/a", Length: 2})

	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	seen := map[string]PackEntry{}
	c.Each(func(k string, e PackEntry) { seen[k] = e })
	if len(seen) != 2 || seen[compactKeyName].Length != 1 || seen["index/latest"].Length != 2 {
		t.Fatalf("Each yielded %v", seen)
	}

	c.Delete(compactKeyName)
	if c.Has(compactKeyName) || c.Len() != 1 {
		t.Errorf("Delete left %d entries, Has=%v", c.Len(), c.Has(compactKeyName))
	}
	c.Delete("index/latest")
	if c.Len() != 0 {
		t.Errorf("Len = %d after deleting both", c.Len())
	}
}

// Pack refs are shared, which is where interning's benefit came from.
func TestPackCatalog_SharesPackRefs(t *testing.T) {
	c := newPackCatalog()
	ref := "packs/" + fmt.Sprintf("%064x", 9)
	for i := 0; i < 100; i++ {
		c.Set("filemeta/"+fmt.Sprintf("%064x", i), PackEntry{PackRef: ref, Length: 1})
	}
	if len(c.packRefs) != 1 {
		t.Fatalf("stored %d distinct pack refs for one pack", len(c.packRefs))
	}
}

// Every namespace this store bundles must be one objkey can encode. Nothing
// breaks if that stops holding — the catalog keeps such keys exact in its
// fallback map — but the compact representation is why the catalog fits in
// memory at repository scale, and a packable namespace missing from the
// encoding table would move every one of its entries to the string-keyed side
// with nothing to say so.
func TestPackablePrefixesAreAllEncodable(t *testing.T) {
	namespaces := objkey.Namespaces()
	encodable := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		encodable[ns] = true
	}
	for _, prefix := range packablePrefixes {
		if !encodable[prefix] {
			t.Errorf("packable prefix %q is not in objkey.Namespaces, so its entries "+
				"fall back to string keys", prefix)
		}
	}
}

// A key that is not canonical lowercase hex must come back exactly as it went
// in. The catalog decoded with encoding/hex before, which accepts both cases, so
// an uppercase key round-tripped through Each as its lowercase spelling — a
// different key, handed to prune's sweep as the name of an object that is not
// there.
func TestPackCatalog_PreservesNonCanonicalHexExactly(t *testing.T) {
	c := newPackCatalog()
	key := "chunk/" + strings.ToUpper(strings.Repeat("ab", 32))
	want := PackEntry{PackRef: "packs/z", Offset: 16, Length: 8}
	c.Set(key, want)

	var got []string
	c.Each(func(k string, _ PackEntry) { got = append(got, k) })
	if len(got) != 1 || got[0] != key {
		t.Fatalf("Each yielded %q, want [%q]", got, key)
	}

	lower := strings.ToLower(key)
	if _, ok := c.Get(lower); ok {
		t.Errorf("%q was reported as present after only %q was set", lower, key)
	}
}
