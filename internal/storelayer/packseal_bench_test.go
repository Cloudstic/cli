package storelayer

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/pkg/store/local"
)

func benchEntries(n int) map[string]PackEntry {
	entries := make(map[string]PackEntry, n)
	for i := range n {
		// Keys are 64 hex characters in a real repository.
		entries[fmt.Sprintf("filemeta/%064x", i)] = PackEntry{
			PackRef: fmt.Sprintf("packs/%064x", i%16),
			Offset:  int64(i * 128),
			Length:  128,
		}
	}
	return entries
}

// Footer construction: the per-pack cost, paid once per 8 MB pack.
func BenchmarkBuildPackFooter(b *testing.B) {
	key := make([]byte, 32)
	for _, n := range []int{100, 10_000} {
		entries := benchEntries(n)
		for _, tc := range []struct {
			name string
			key  []byte
		}{{"plain", nil}, {"sealed", key}} {
			b.Run(fmt.Sprintf("entries=%d/%s", n, tc.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := buildPackFooter(entries, tc.key); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// Catalog flush: the repository-wide cost, paid once per flush. This is the
// path that scales with total repository size rather than pack size.
func BenchmarkPackCatalogFlush(b *testing.B) {
	ctx := context.Background()
	key := make([]byte, 32)

	for _, n := range []int{10_000, 200_000} {
		for _, tc := range []struct {
			name string
			key  []byte
		}{{"plain", nil}, {"sealed", key}} {
			b.Run(fmt.Sprintf("entries=%d/%s", n, tc.name), func(b *testing.B) {
				base, err := local.New(b.TempDir())
				if err != nil {
					b.Fatal(err)
				}
				var opts []PackOption
				if tc.key != nil {
					opts = append(opts, WithPackIndexKey(tc.key))
				}
				ps, err := NewPackStore(base, opts...)
				if err != nil {
					b.Fatal(err)
				}
				entries := benchEntries(n)
				ps.mu.Lock()
				ps.catalog = catalogOf(entries)
				ps.catalogLoaded = true
				ps.mu.Unlock()

				b.ReportAllocs()
				for b.Loop() {
					ps.mu.Lock()
					ps.catalog = catalogOf(entries)
					ps.pendingKeys = keySetOf(entries)
					ps.mu.Unlock()
					if err := ps.Flush(ctx); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// Catalog load, including the unseal step.
func BenchmarkPackCatalogLoad(b *testing.B) {
	ctx := context.Background()
	key := make([]byte, 32)

	for _, n := range []int{10_000, 200_000} {
		for _, tc := range []struct {
			name string
			key  []byte
		}{{"plain", nil}, {"sealed", key}} {
			b.Run(fmt.Sprintf("entries=%d/%s", n, tc.name), func(b *testing.B) {
				dir := b.TempDir()
				base, err := local.New(dir)
				if err != nil {
					b.Fatal(err)
				}
				var opts []PackOption
				if tc.key != nil {
					opts = append(opts, WithPackIndexKey(tc.key))
				}
				writer, err := NewPackStore(base, opts...)
				if err != nil {
					b.Fatal(err)
				}
				entries := benchEntries(n)
				writer.mu.Lock()
				writer.catalog = catalogOf(entries)
				writer.pendingKeys = keySetOf(entries)
				writer.catalogLoaded = true
				writer.mu.Unlock()
				if err := writer.Flush(ctx); err != nil {
					b.Fatal(err)
				}

				b.ReportAllocs()
				for b.Loop() {
					reader, err := NewPackStore(base, opts...)
					if err != nil {
						b.Fatal(err)
					}
					if err := reader.ensureCatalogLoaded(ctx); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// keySetOf names every entry in a catalog, as pendingKeys does for the entries a
// run has added.
func keySetOf(entries map[string]PackEntry) map[string]struct{} {
	keys := make(map[string]struct{}, len(entries))
	for key := range entries {
		keys[key] = struct{}{}
	}
	return keys
}
