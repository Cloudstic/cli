package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// rangePayload is deliberately longer than any single range under test, so a
// backend that ignores the range and returns the whole object fails rather than
// coincidentally matching.
var rangePayload = []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// assertRangeGetterConformance is the shared contract every RangeGetter must
// satisfy, so the backends cannot drift apart. Each is exercised against a live
// instance of its own backend.
func assertRangeGetterConformance(t *testing.T, s ObjectStore) {
	t.Helper()
	ctx := context.Background()

	ranger, ok := s.(RangeGetter)
	if !ok {
		t.Fatalf("%T does not implement RangeGetter", s)
	}

	const key = "chunk/rangetest"
	if err := s.Put(ctx, key, rangePayload); err != nil {
		t.Fatalf("put: %v", err)
	}

	t.Run("agrees with a full Get", func(t *testing.T) {
		full, err := s.Get(ctx, key)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !bytes.Equal(full, rangePayload) {
			t.Fatalf("full Get returned %d bytes, want %d", len(full), len(rangePayload))
		}

		for _, tc := range []struct {
			name           string
			offset, length int64
		}{
			{"from the start", 0, 5},
			{"from the middle", 10, 6},
			{"a single byte", 31, 1},
			{"ending exactly at the object end", int64(len(rangePayload)) - 4, 4},
			{"the whole object", 0, int64(len(rangePayload))},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := ranger.GetRange(ctx, key, tc.offset, tc.length)
				if err != nil {
					t.Fatalf("GetRange(%d,%d): %v", tc.offset, tc.length, err)
				}
				want := full[tc.offset : tc.offset+tc.length]
				if !bytes.Equal(got, want) {
					t.Errorf("GetRange(%d,%d) = %q, want %q", tc.offset, tc.length, got, want)
				}
			})
		}
	})

	t.Run("reading past the end is an error", func(t *testing.T) {
		if got, err := ranger.GetRange(ctx, key, int64(len(rangePayload))-2, 10); err == nil {
			t.Errorf("expected an error, got %d bytes: %q", len(got), got)
		}
	})

	t.Run("a zero-length range is empty, not an error", func(t *testing.T) {
		got, err := ranger.GetRange(ctx, key, 4, 0)
		if err != nil {
			t.Fatalf("zero-length range: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected no bytes, got %q", got)
		}
	})

	t.Run("a negative range is rejected", func(t *testing.T) {
		if _, err := ranger.GetRange(ctx, key, -1, 4); err == nil {
			t.Error("expected an error for a negative offset")
		}
	})

	t.Run("a missing object is an error", func(t *testing.T) {
		if _, err := ranger.GetRange(ctx, "chunk/does-not-exist", 0, 4); err == nil {
			t.Error("expected an error for a missing object")
		}
	})
}

func TestLocalStore_RangeGetterConformance(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertRangeGetterConformance(t, s)
}

// nonRangeStore is an ObjectStore with no ranged read, used to check the
// fallback paths in wrappers that must not assume one.
type nonRangeStore struct{ ObjectStore }

func TestDebugStore_RangeGetterConformance(t *testing.T) {
	inner, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("over a backend that ranges", func(t *testing.T) {
		assertRangeGetterConformance(t, NewDebugStore(inner, &strings.Builder{}))
	})

	t.Run("over a backend that does not", func(t *testing.T) {
		plain, err := NewLocalStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		assertRangeGetterConformance(t, NewDebugStore(&nonRangeStore{ObjectStore: plain}, &strings.Builder{}))
	})
}

// countingRangeStore records how each read reached the backend.
type countingRangeStore struct {
	ObjectStore
	fullGets   int
	rangeGets  int
	bytesRead  int64
	rangeCalls []int64
}

func (c *countingRangeStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := c.ObjectStore.Get(ctx, key)
	if strings.HasPrefix(key, packPrefix) {
		c.fullGets++
		c.bytesRead += int64(len(data))
	}
	return data, err
}

func (c *countingRangeStore) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	ranger, ok := c.ObjectStore.(RangeGetter)
	if !ok {
		return nil, errors.New("inner store cannot range")
	}
	data, err := ranger.GetRange(ctx, key, offset, length)
	if strings.HasPrefix(key, packPrefix) {
		c.rangeGets++
		c.bytesRead += int64(len(data))
		c.rangeCalls = append(c.rangeCalls, length)
	}
	return data, err
}

// The point of RangeGetter is that PackStore actually uses it. A backend that
// implements it while PackStore keeps pulling whole packs would look correct
// and change nothing, so assert the reads really are ranged and really are
// small.
func TestPackStore_UsesRangedReadsForFooters(t *testing.T) {
	ctx := context.Background()

	base, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingRangeStore{ObjectStore: base}

	writer, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	// Enough payload that a whole-pack transfer is obviously distinguishable
	// from a footer read.
	payload := bytes.Repeat([]byte("x"), 64*1024)
	for _, key := range []string{"filemeta/a", "filemeta/b", "node/c"} {
		if err := writer.Put(ctx, key, payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	packs, err := base.List(ctx, packPrefix)
	if err != nil || len(packs) != 1 {
		t.Fatalf("expected 1 pack, got %v (err %v)", packs, err)
	}
	packSize, err := base.Size(ctx, packs[0])
	if err != nil {
		t.Fatal(err)
	}

	// A fresh store, so nothing is served from the in-memory pack cache.
	reader, err := NewPackStore(counting)
	if err != nil {
		t.Fatal(err)
	}
	counting.fullGets, counting.rangeGets, counting.bytesRead = 0, 0, 0
	counting.rangeCalls = nil

	if _, _, err := reader.RebuildCatalog(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if counting.rangeGets == 0 {
		t.Error("rebuilding read no ranges: PackStore is not using RangeGetter")
	}
	if counting.fullGets != 0 {
		t.Errorf("rebuilding pulled %d whole packfiles; footers should be read by range", counting.fullGets)
	}
	if counting.bytesRead >= packSize {
		t.Errorf("rebuilding transferred %d bytes of a %d byte pack; the ranged path is not saving anything",
			counting.bytesRead, packSize)
	}
	t.Logf("rebuild read %d bytes via %d ranged reads (%v) from a %d byte pack",
		counting.bytesRead, counting.rangeGets, counting.rangeCalls, packSize)
}
