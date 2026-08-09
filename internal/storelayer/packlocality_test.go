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

// flatten concatenates groups, for the properties that are about order rather
// than about where the boundaries fall.
func flatten(groups [][]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// assertPartition is the invariant every other property rests on: grouping
// redistributes keys and nothing else. A caller hands over the keys it intends
// to read, so dropping one silently loses an object and duplicating one reads it
// twice. Empty groups are rejected too — a caller may make the group its unit of
// concurrency, and an empty one buys a goroutine that does nothing.
func assertPartition(t *testing.T, in []string, groups [][]string) {
	t.Helper()
	for i, g := range groups {
		if len(g) == 0 {
			t.Fatalf("group %d is empty", i)
		}
	}
	assertPermutation(t, in, flatten(groups))
}

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
func TestPackStore_PlanReadsClustersKeysSharingAPack(t *testing.T) {
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

	groups := w.PlanReads(ctx, scattered).Groups
	assertPartition(t, scattered, groups)
	got := flatten(groups)

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
func TestPackStore_PlanReadsOrdersByOffsetWithinAPack(t *testing.T) {
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

	groups := w.PlanReads(ctx, reversed).Groups
	assertPartition(t, reversed, groups)
	got := flatten(groups)

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
func TestPackStore_PlanReadsKeepsUnknownKeys(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, w := seedPacks(t, ctx, base, 2, 4)

	mixed := []string{"chunk/unknown-a", keys[3], "chunk/unknown-b", keys[0], "chunk/unknown-c"}
	groups := w.PlanReads(ctx, mixed).Groups
	assertPartition(t, mixed, groups)
	got := flatten(groups)

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
func TestPlanReads_WalksTheWrapperChain(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, w := seedPacks(t, ctx, base, 3, 6)

	// No grouper anywhere in the chain: the input comes back untouched.
	if got := flatten(store.PlanReads(ctx, base, keys).Groups); len(got) != len(keys) {
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
	got := flatten(store.PlanReads(ctx, chain, scattered).Groups)

	direct := flatten(w.PlanReads(ctx, scattered).Groups)
	for i := range direct {
		if got[i] != direct[i] {
			t.Fatalf("grouping through the chain differs from grouping directly: %v vs %v", got, direct)
		}
	}
}

// Grouping loads the catalog, and this test asserts the reverse of what it used
// to. Grouping was a hint a caller could ignore, so forcing I/O for it was
// wrong. It is now the mechanism admission runs on: without a catalog every key
// looks unpacked, the store falls back to probing, and the caller pays the
// probe sequence with nothing to indicate why. A caller groups because it is
// about to read these keys, so the load is work it had already committed to.
func TestPackStore_PlanReadsLoadsTheCatalog(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, w := seedPacks(t, ctx, base, 2, 12)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if reader.catalogLoaded {
		t.Fatal("catalog already loaded before grouping; the test proves nothing")
	}

	scattered := []string{keys[13], keys[0], keys[14], keys[1]}
	groups := reader.PlanReads(ctx, scattered).Groups
	assertPartition(t, scattered, groups)

	if !reader.catalogLoaded {
		t.Error("grouping did not load the catalog, so admission has nothing to work from")
	}
	if len(groups) != 2 {
		t.Errorf("got %d groups for keys spanning 2 packs: %v", len(groups), groups)
	}
}

// A backend that cannot answer must still return something usable, and it must
// be singletons: a caller may make the group its unit of concurrency, so one
// group of everything would serialise it while claiming a locality that was
// never established.
func TestPackStore_PlanReadsReturnsSingletonsWithoutACatalog(t *testing.T) {
	ctx := context.Background()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	in := []string{"filemeta/a", "filemeta/b", "filemeta/c"}
	groups := reader.PlanReads(ctx, in).Groups
	assertPartition(t, in, groups)
	if len(groups) != len(in) {
		t.Fatalf("got %d groups for %d unpacked keys; want one each", len(groups), len(in))
	}
	for i, g := range groups {
		if g[0] != in[i] {
			t.Fatalf("with nothing known the input order should survive, got %v", groups)
		}
	}
}
