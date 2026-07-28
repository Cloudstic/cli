package storelayer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

type countingRangeStore struct {
	store.ObjectStore
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
	ranger, ok := c.ObjectStore.(store.RangeGetter)
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

// The point of store.RangeGetter is that PackStore actually uses it. A backend that
// implements it while PackStore keeps pulling whole packs would look correct
// and change nothing, so assert the reads really are ranged and really are
// small.
func TestPackStore_UsesRangedReadsForFooters(t *testing.T) {
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
		t.Error("rebuilding read no ranges: PackStore is not using store.RangeGetter")
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
