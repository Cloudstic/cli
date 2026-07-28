package storetest

import (
	"bytes"
	"context"
	"testing"
)

// RangeGetter mirrors store.RangeGetter. It is redeclared here, rather than
// imported, for the same reason ObjectStore is: pkg/store's own internal tests
// import this package, so depending on pkg/store would make the test binary
// import itself.
type RangeGetter interface {
	GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error)
}

// rangePayload is deliberately longer than any single range under test, so a
// backend that ignores the range and returns the whole object fails rather than
// coincidentally matching.
var rangePayload = []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// assertRangeGetterConformance is the shared contract every RangeGetter must
// satisfy, so the backends cannot drift apart. Each is exercised against a live
// instance of its own backend.
// AssertRangeGetterConformance is exported so every backend package can hold
// itself to the same contract.
func AssertRangeGetterConformance(t *testing.T, s ObjectStore) {
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
