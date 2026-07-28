package store_test

import (
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

func TestLocalStore_RangeGetterConformance(t *testing.T) {
	s, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storetest.AssertRangeGetterConformance(t, s)
}

// nonRangeStore is an store.ObjectStore with no ranged read, used to check the
// fallback paths in wrappers that must not assume one.
type nonRangeStore struct{ store.ObjectStore }

func TestDebugStore_RangeGetterConformance(t *testing.T) {
	inner, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("over a backend that ranges", func(t *testing.T) {
		storetest.AssertRangeGetterConformance(t, store.NewDebugStore(inner, &strings.Builder{}))
	})

	t.Run("over a backend that does not", func(t *testing.T) {
		plain, err := local.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		storetest.AssertRangeGetterConformance(t, store.NewDebugStore(&nonRangeStore{ObjectStore: plain}, &strings.Builder{}))
	})
}

// countingRangeStore records how each read reached the backend.
