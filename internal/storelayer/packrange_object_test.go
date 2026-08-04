package storelayer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// seedPackWithObjects writes n objects of the given size through a PackStore and
// flushes, returning the keys and the counting store wrapping the backend.
func seedPackWithObjects(t *testing.T, ctx context.Context, n, size int) ([]string, *countingRangeStore, *PackStore) {
	t.Helper()

	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingRangeStore{ObjectStore: base}
	writer, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}

	payload := bytes.Repeat([]byte("x"), size)
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("filemeta/%064x", i)
		if err := writer.Put(ctx, key, payload); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	counting.fullGets, counting.rangeGets, counting.bytesRead = 0, 0, 0
	counting.rangeCalls = nil
	return keys, counting, reader
}

// A one-off read must transfer the object, not the packfile around it. This is
// the sampled pattern: `cat` of a single file, or restore touching a pack once.
func TestPackStore_ReadsOneObjectWithARangedRead(t *testing.T) {
	ctx := context.Background()
	keys, counting, reader := seedPackWithObjects(t, ctx, 40, 16*1024)

	got, err := reader.Get(ctx, keys[7])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 16*1024 {
		t.Fatalf("got %d bytes, want %d", len(got), 16*1024)
	}

	if counting.fullGets != 0 {
		t.Errorf("transferred %d whole packfiles for a single object", counting.fullGets)
	}
	if counting.rangeGets != 1 {
		t.Errorf("ranged reads = %d, want 1", counting.rangeGets)
	}
	// The pack holds 40 x 16 KB; a ranged read must be about one object.
	if counting.bytesRead > 64*1024 {
		t.Errorf("read %d bytes to return %d", counting.bytesRead, len(got))
	}
}

// A scan of the same pack must not degrade into one request per object. After a
// couple of misses the pack is fetched whole and cached, so the rest are served
// from memory -- which is what makes check, ls and prune cheap.
func TestPackStore_PromotesToWholePackWhenScanning(t *testing.T) {
	ctx := context.Background()
	keys, counting, reader := seedPackWithObjects(t, ctx, 40, 16*1024)

	for _, key := range keys {
		if _, err := reader.Get(ctx, key); err != nil {
			t.Fatalf("Get(%s): %v", key, err)
		}
	}

	if counting.fullGets != 1 {
		t.Errorf("whole-pack transfers = %d, want 1 (a scan should promote once)", counting.fullGets)
	}
	if counting.rangeGets >= len(keys) {
		t.Errorf("ranged reads = %d for %d objects; the scan never promoted", counting.rangeGets, len(keys))
	}
	// Exactly the configured boundary: fewer would mean promotion fired early
	// (and zero would mean ranging never happened at all, which these checks
	// must not accept), more would mean it never fired.
	if want := packPromoteAfter - 1; counting.rangeGets != want {
		t.Errorf("ranged reads before promotion = %d, want %d", counting.rangeGets, want)
	}
}

// Sampling many packs must stay ranged rather than promoting each one, which is
// the pattern that made restore expensive.
func TestPackStore_SampledReadsAcrossPacksStayRanged(t *testing.T) {
	ctx := context.Background()

	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingRangeStore{ObjectStore: base}
	writer, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}

	// One object per pack: flushing after each Put forces a pack boundary.
	payload := bytes.Repeat([]byte("y"), 8*1024)
	var keys []string
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("filemeta/%064x", i)
		if err := writer.Put(ctx, key, payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}

	reader, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	counting.fullGets, counting.rangeGets, counting.bytesRead = 0, 0, 0

	for _, key := range keys {
		if _, err := reader.Get(ctx, key); err != nil {
			t.Fatalf("Get(%s): %v", key, err)
		}
	}

	if counting.fullGets != 0 {
		t.Errorf("whole-pack transfers = %d; touching each pack once should stay ranged", counting.fullGets)
	}
	if counting.rangeGets != len(keys) {
		t.Errorf("ranged reads = %d, want %d", counting.rangeGets, len(keys))
	}
}

// A backend without RangeGetter keeps the old behaviour, which is correct
// everywhere and merely slower.
func TestPackStore_FallsBackToWholePackWithoutRangeGetter(t *testing.T) {
	ctx := context.Background()

	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plain := &noRangeStore{ObjectStore: base}
	writer, err := NewPackStore(plain)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("filemeta/%064x", 1)
	payload := bytes.Repeat([]byte("z"), 16*1024)
	if err := writer.Put(ctx, key, payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %d bytes, want %d", len(got), len(payload))
	}
}

// noRangeStore hides an inner store's GetRange, so PackStore must take the
// whole-pack path.
type noRangeStore struct{ store.ObjectStore }

func (n *noRangeStore) Unwrap() store.ObjectStore { return nil }

// A DebugStore over a backend that cannot range satisfies store.RangeGetter but
// emulates it with a full transfer. Treating that as native would make every
// miss before promotion download the whole pack *and* skip the body cache --
// worse than never ranging. The whole-pack path must be taken instead.
func TestPackStore_DoesNotRangeThroughAnEmulatingWrapper(t *testing.T) {
	ctx := context.Background()

	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// noRangeStore hides local's GetRange; DebugStore then re-declares it and
	// emulates, which is exactly the shape being guarded against.
	emulating := store.NewDebugStore(&noRangeStore{ObjectStore: base}, io.Discard)
	if _, ok := interface{}(emulating).(store.RangeGetter); !ok {
		t.Fatal("fixture is wrong: DebugStore should satisfy RangeGetter")
	}
	if rangesNatively(emulating) {
		t.Fatal("emulated ranging was reported as native")
	}

	counting := &countingRangeStore{ObjectStore: emulating}
	writer, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("filemeta/%064x", 3)
	payload := bytes.Repeat([]byte("w"), 16*1024)
	if err := writer.Put(ctx, key, payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	counting.fullGets, counting.rangeGets, counting.bytesRead = 0, 0, 0

	got, err := reader.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %d bytes, want %d", len(got), len(payload))
	}
	if counting.rangeGets != 0 {
		t.Errorf("ranged reads = %d through an emulating wrapper, want 0", counting.rangeGets)
	}
	if counting.fullGets != 1 {
		t.Errorf("whole-pack transfers = %d, want 1", counting.fullGets)
	}

	// And the pack must be cached, so a second read costs nothing.
	if _, err := reader.Get(ctx, key); err != nil {
		t.Fatal(err)
	}
	if counting.fullGets != 1 {
		t.Errorf("whole-pack transfers = %d after a second read; the pack was not cached", counting.fullGets)
	}
}

// A native ranger behind a DebugStore is still native -- --debug must not turn
// ranged reads off any more than it should turn them on.
func TestPackStore_RangesThroughADebugStoreOverANativeRanger(t *testing.T) {
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !rangesNatively(store.NewDebugStore(base, io.Discard)) {
		t.Error("a native ranger behind DebugStore was reported as emulated")
	}
}
