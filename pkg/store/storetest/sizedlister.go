package storetest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
)

// SizedLister mirrors store.SizedLister. It is redeclared here, rather than
// imported, for the same reason ObjectStore and RangeGetter are: pkg/store's
// own internal tests import this package, so depending on pkg/store would make
// the test binary import itself.
type SizedLister interface {
	ListSized(ctx context.Context, prefix string, fn func(key string, size int64) error) error
}

// AssertSizedListerConformance is the shared contract every SizedLister must
// satisfy, so the backends cannot drift apart. Each is exercised against a live
// instance of its own backend.
//
// The cases are what a caller that accounts from a listing depends on: that
// the sized listing names exactly the keys List names, that each size is the
// one Size reports, and that an error from the callback stops the listing and
// comes back — a backend that swallowed it would hand a caller a set it had
// stopped reading part-way through.
func AssertSizedListerConformance(t *testing.T, s ObjectStore) {
	t.Helper()
	ctx := context.Background()

	lister, ok := s.(SizedLister)
	if !ok {
		t.Fatalf("%T does not implement SizedLister", s)
	}

	const prefix = "chunk/sizedlist-"
	want := map[string]int64{}
	for i, n := range []int{1, 17, 300, 4096} {
		key := fmt.Sprintf("%s%d", prefix, i)
		if err := s.Put(ctx, key, make([]byte, n)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
		want[key] = int64(n)
	}
	// A neighbour outside the prefix, which the listing must not include.
	if err := s.Put(ctx, "chunk/sizedlist_other", []byte("xx")); err != nil {
		t.Fatalf("put neighbour: %v", err)
	}
	t.Cleanup(func() {
		for key := range want {
			_ = s.Delete(ctx, key)
		}
		_ = s.Delete(ctx, "chunk/sizedlist_other")
	})

	t.Run("names the keys List names, with the sizes Size reports", func(t *testing.T) {
		got := map[string]int64{}
		err := lister.ListSized(ctx, prefix, func(key string, size int64) error {
			if _, dup := got[key]; dup {
				t.Errorf("%s was listed twice", key)
			}
			got[key] = size
			return nil
		})
		if err != nil {
			t.Fatalf("ListSized: %v", err)
		}

		listed, err := s.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		sort.Strings(listed)
		gotKeys := make([]string, 0, len(got))
		for key := range got {
			gotKeys = append(gotKeys, key)
		}
		sort.Strings(gotKeys)
		if fmt.Sprint(gotKeys) != fmt.Sprint(listed) {
			t.Errorf("ListSized keys = %v, List = %v", gotKeys, listed)
		}

		for key, size := range got {
			viaSize, err := s.Size(ctx, key)
			if err != nil {
				t.Fatalf("Size(%s): %v", key, err)
			}
			if size != viaSize {
				t.Errorf("%s listed at %d bytes, Size reports %d", key, size, viaSize)
			}
			if size != want[key] {
				t.Errorf("%s listed at %d bytes, %d were written", key, size, want[key])
			}
		}
	})

	t.Run("an error from the callback stops the listing", func(t *testing.T) {
		stop := errors.New("stop")
		calls := 0
		err := lister.ListSized(ctx, prefix, func(string, int64) error {
			calls++
			return stop
		})
		if !errors.Is(err, stop) {
			t.Errorf("ListSized returned %v, want the callback's error", err)
		}
		if calls != 1 {
			t.Errorf("the callback was called %d times after asking to stop, want 1", calls)
		}
	})

	t.Run("a prefix nothing is under lists nothing", func(t *testing.T) {
		err := lister.ListSized(ctx, "chunk/sizedlist-nothing-here-", func(key string, _ int64) error {
			t.Errorf("listed %s under an empty prefix", key)
			return nil
		})
		if err != nil {
			t.Errorf("ListSized over an empty prefix: %v", err)
		}
	})
}
