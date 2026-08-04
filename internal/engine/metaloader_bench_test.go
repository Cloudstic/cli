package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// populateMetas writes n distinct filemeta objects and returns their refs in
// insertion order.
func populateMetas(tb testing.TB, s *MockStore, n int) []string {
	tb.Helper()
	ctx := context.Background()
	refs := make([]string, n)
	for i := range n {
		meta := core.FileMeta{
			Version:     1,
			FileID:      fmt.Sprintf("file-%d", i),
			Name:        fmt.Sprintf("document-%d.txt", i),
			Type:        core.FileTypeFile,
			Parents:     []string{fmt.Sprintf("folder-%d", i%128)},
			ContentHash: fmt.Sprintf("%064x", i),
			Size:        int64(i),
			Mtime:       int64(1700000000 + i),
		}
		ref, data, err := core.FileMetaRef(&meta)
		if err != nil {
			tb.Fatalf("FileMetaRef: %v", err)
		}
		if err := s.Put(ctx, ref, data); err != nil {
			tb.Fatalf("Put: %v", err)
		}
		refs[i] = ref
	}
	return refs
}

// retainedBytes reports the heap still held after fn returns, with the value it
// produced kept alive across the measurement.
func retainedBytes(fn func() any) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	held := fn()

	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(held)

	if after.HeapAlloc < before.HeapAlloc {
		return 0
	}
	return after.HeapAlloc - before.HeapAlloc
}

// BenchmarkMetaLoaderRetained reports how much heap a loader still holds after
// reading n distinct filemetas. It is the data behind the files-to-memory table
// in docs/caching.md: a bounded loader must flatten as n grows, an unbounded one
// climbs linearly.
//
// The store is populated before measurement starts, so what is reported is the
// loader's own retention rather than the fixture's.
func BenchmarkMetaLoaderRetained(b *testing.B) {
	for _, n := range []int{1000, 5000, 20000, 100000, 250000} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			ctx := context.Background()
			s := NewMockStore()
			refs := populateMetas(b, s, n)

			var held uint64
			for b.Loop() {
				held = retainedBytes(func() any {
					l := newMetaLoader(s)
					for _, ref := range refs {
						if _, err := l.load(ctx, ref); err != nil {
							b.Fatalf("load: %v", err)
						}
					}
					return l
				})
			}

			b.ReportMetric(float64(held)/1e6, "MB-retained")
			b.ReportMetric(float64(held)/float64(n), "B/file")
		})
	}
}

// BenchmarkMetaLoaderDiffPattern exercises the access pattern the cache exists
// for: several consecutive snapshots over one tree, where an unchanged file
// keeps its filemeta and so recurs across every snapshot. churn is the fraction
// of files that change between snapshots.
//
// This is what sizing the cache depends on — a bound below the working set
// turns those repeats back into store reads.
func BenchmarkMetaLoaderDiffPattern(b *testing.B) {
	const snapshots = 8
	for _, files := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			ctx := context.Background()
			s := NewMockStore()
			refs := populateMetas(b, s, files)

			counting := &countingStore{MockStore: s}
			for b.Loop() {
				counting.gets = 0
				l := newMetaLoader(counting)
				for range snapshots {
					for _, ref := range refs {
						if _, err := l.load(ctx, ref); err != nil {
							b.Fatalf("load: %v", err)
						}
					}
				}
			}

			total := files * snapshots
			b.ReportMetric(float64(counting.gets), "store-reads")
			b.ReportMetric(100*(1-float64(counting.gets)/float64(total)), "%hit")
		})
	}
}

// countingStore counts the reads that actually reach the store, which is how
// the cache's hit rate is observed from outside it.
type countingStore struct {
	*MockStore
	gets int
}

func (s *countingStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.gets++
	return s.MockStore.Get(ctx, key)
}

// sanity check that the fixture decodes, so a benchmark failure is never a
// broken fixture masquerading as a performance result.
func TestPopulateMetasRoundTrips(t *testing.T) {
	s := NewMockStore()
	refs := populateMetas(t, s, 4)

	data, err := s.Get(context.Background(), refs[2])
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var fm core.FileMeta
	if err := json.Unmarshal(data, &fm); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if fm.Name != "document-2.txt" {
		t.Errorf("Name = %q, want document-2.txt", fm.Name)
	}
}
