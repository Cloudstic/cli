package storelayer

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// assertPermutation is the invariant every other property rests on: grouping is
// a reordering and nothing else. A caller hands over the keys it intends to
// read, so dropping one silently loses an object and duplicating one reads it
// twice.
func assertPermutation(t *testing.T, in, out []string) {
	t.Helper()
	if len(in) != len(out) {
		t.Fatalf("grouping returned %d keys for %d input keys", len(out), len(in))
	}
	a := append([]string(nil), in...)
	b := append([]string(nil), out...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("grouping is not a permutation: %q became %q", a[i], b[i])
		}
	}
}

// seedPacks writes perPack objects into each of packs packfiles and returns the
// keys in the order they were written.
func seedPacks(t *testing.T, ctx context.Context, base store.ObjectStore, packs, perPack int) ([]string, *PackStore) {
	t.Helper()
	w, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("p"), 4*1024)
	var keys []string
	for p := 0; p < packs; p++ {
		for i := 0; i < perPack; i++ {
			key := fmt.Sprintf("filemeta/%064x", p*1000+i)
			if err := w.Put(ctx, key, payload); err != nil {
				t.Fatal(err)
			}
			keys = append(keys, key)
		}
		if err := w.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return keys, w
}

// The property the change exists for: keys that share a pack come out adjacent,
// so a reader working through the result transfers each pack once instead of
// returning to it.
func TestPackStore_GroupByLocalityClustersKeysSharingAPack(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const packs, perPack = 5, 8
	keys, w := seedPacks(t, ctx, base, packs, perPack)

	// Interleave across packs, which is what a walk over a repository built by
	// several backups produces.
	var scattered []string
	for i := 0; i < perPack; i++ {
		for p := 0; p < packs; p++ {
			scattered = append(scattered, keys[p*perPack+i])
		}
	}

	got := w.GroupByLocality(scattered)
	assertPermutation(t, scattered, got)

	// Count how many times the result crosses from one pack to another. Grouped
	// perfectly that is packs-1; scattered it is close to len(keys)-1.
	packOf := func(key string) string {
		entry, ok := w.catalog.Get(key)
		if !ok {
			t.Fatalf("key %s is not in the catalog", key)
		}
		return entry.PackRef
	}
	transitions := 0
	for i := 1; i < len(got); i++ {
		if packOf(got[i]) != packOf(got[i-1]) {
			transitions++
		}
	}
	if transitions != packs-1 {
		t.Errorf("result crosses packs %d times, want %d (one run per pack)", transitions, packs-1)
	}
	if before := packs*perPack - 1; transitions >= before {
		t.Errorf("grouping did not reduce pack transitions at all (%d vs %d scattered)", transitions, before)
	}
}

// Within a pack, reads should run forwards rather than seeking around.
func TestPackStore_GroupByLocalityOrdersByOffsetWithinAPack(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, w := seedPacks(t, ctx, base, 1, 12)

	reversed := make([]string, len(keys))
	for i, k := range keys {
		reversed[len(keys)-1-i] = k
	}

	got := w.GroupByLocality(reversed)
	assertPermutation(t, reversed, got)

	last := int64(-1)
	for _, k := range got {
		entry, ok := w.catalog.Get(k)
		if !ok {
			t.Fatalf("key %s is not in the catalog", k)
		}
		if entry.Offset < last {
			t.Fatalf("offsets are not ascending within the pack: %d after %d", entry.Offset, last)
		}
		last = entry.Offset
	}
}

// Keys the catalog knows nothing about must survive, so a caller can hand over
// a mixed set without first working out which is which.
func TestPackStore_GroupByLocalityKeepsUnknownKeys(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, w := seedPacks(t, ctx, base, 2, 4)

	mixed := []string{"chunk/unknown-a", keys[3], "chunk/unknown-b", keys[0], "chunk/unknown-c"}
	got := w.GroupByLocality(mixed)
	assertPermutation(t, mixed, got)

	// Unknown keys keep their relative order among themselves.
	var unknown []string
	for _, k := range got {
		if _, packed := w.catalog.Get(k); !packed {
			unknown = append(unknown, k)
		}
	}
	want := []string{"chunk/unknown-a", "chunk/unknown-b", "chunk/unknown-c"}
	for i := range want {
		if unknown[i] != want[i] {
			t.Fatalf("unknown keys reordered: got %v, want %v", unknown, want)
		}
	}
}

// A store that cannot group returns the caller's order, and the helper has to
// find PackStore underneath the wrappers that sit above it in the real chain.
func TestGroupByLocality_WalksTheWrapperChain(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, w := seedPacks(t, ctx, base, 3, 6)

	// No grouper anywhere in the chain: the input comes back untouched.
	if got := store.GroupByLocality(base, keys); len(got) != len(keys) {
		t.Fatalf("plain backend changed the key count")
	} else {
		for i := range keys {
			if got[i] != keys[i] {
				t.Fatalf("plain backend reordered keys at %d", i)
			}
		}
	}

	// PackStore under the wrappers it really runs under.
	chain := NewCompressedStore(NewEncryptedStore(w, testKey(t)))
	scattered := []string{keys[12], keys[0], keys[6], keys[13], keys[1]}
	got := store.GroupByLocality(chain, scattered)
	assertPermutation(t, scattered, got)

	direct := w.GroupByLocality(scattered)
	for i := range direct {
		if got[i] != direct[i] {
			t.Fatalf("grouping through the chain differs from grouping directly: %v vs %v", got, direct)
		}
	}
}

// Grouping must not force a catalog load. It is a hint issued before any read,
// and turning it into I/O would make a caller pay for advice it can ignore.
func TestPackStore_GroupByLocalityDoesNotLoadTheCatalog(t *testing.T) {
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	in := []string{"filemeta/a", "filemeta/b", "filemeta/c"}
	got := reader.GroupByLocality(in)
	assertPermutation(t, in, got)
	if reader.catalogLoaded {
		t.Error("grouping loaded the catalog; it must stay a no-I/O hint")
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("with no catalog loaded the input order should survive, got %v", got)
		}
	}
}
