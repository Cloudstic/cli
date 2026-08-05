package storelayer

import (
	"context"
	"fmt"
	"runtime"
	"testing"
)

// digestHexKeys returns n distinct content-addressed keys under prefix, in
// the "<prefix><64 hex chars>" shape a real repository uses.
func digestHexKeys(prefix string, n int) []string {
	keys := make([]string, n)
	for i := range n {
		keys[i] = fmt.Sprintf("%s%064x", prefix, i)
	}
	return keys
}

// BenchmarkKeyCachePreload measures allocation for populating the existence
// set at a realistic object count, the operation issue #430 is about: a
// backup preloads chunk/, content/ and node/ once up front.
func BenchmarkKeyCachePreload(b *testing.B) {
	for _, n := range []int{100_000, 500_000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			keys := digestHexKeys("chunk/", n)

			b.ReportAllocs()
			for b.Loop() {
				kc := NewKeyCacheStore(nil)
				kc.mu.Lock()
				for _, key := range keys {
					kc.learnLocked("chunk/", key)
				}
				kc.listedPrefixes["chunk/"] = struct{}{}
				kc.mu.Unlock()
			}
		})
	}
}

// BenchmarkKeyCacheExists measures the steady-state existence check, the
// operation backup's dedup path calls once per object.
func BenchmarkKeyCacheExists(b *testing.B) {
	for _, n := range []int{100_000, 500_000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			ctx := context.Background()
			kc := NewKeyCacheStore(nil)
			keys := digestHexKeys("chunk/", n)
			kc.mu.Lock()
			for _, key := range keys {
				kc.learnLocked("chunk/", key)
			}
			kc.listedPrefixes["chunk/"] = struct{}{}
			kc.mu.Unlock()

			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				if _, err := kc.Exists(ctx, keys[i%n]); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// retainedBytes reports the heap growth attributable to fn's return value,
// via a GC + ReadMemStats delta around the call (mirrors
// internal/engine/metaloader_bench_test.go's helper of the same name — see
// that file and docs/caching.md for why this, and not -benchmem's cumulative
// B/op, is the right metric for a retained set's footprint: B/op counts every
// allocation regardless of whether it survives, so it cannot see memory a
// different representation lets the GC reclaim.
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

// BenchmarkKeySetShapeRetained is a direct, KeyCacheStore-independent
// comparison of the two set shapes: a map keyed by the full hex string (what
// this store used before #430) against a map keyed by the raw 32-byte digest
// (what it uses now). Each key string is freshly generated inside the
// measured region, as inner.List would return it, so the string variant pays
// for retaining that backing array (it is the map key) while the digest
// variant does not (it decodes and discards it) — that discard, not bucket
// layout alone, is most of the win in practice.
func BenchmarkKeySetShapeRetained(b *testing.B) {
	for _, n := range []int{100_000, 500_000} {
		b.Run(fmt.Sprintf("string/entries=%d", n), func(b *testing.B) {
			var held uint64
			for b.Loop() {
				held = retainedBytes(func() any {
					keys := digestHexKeys("chunk/", n)
					set := make(map[string]struct{}, n)
					for _, key := range keys {
						set[key] = struct{}{}
					}
					return set
				})
			}
			b.ReportMetric(float64(held)/1e6, "MB-retained")
			b.ReportMetric(float64(held)/float64(n), "B/entry")
		})

		b.Run(fmt.Sprintf("digest/entries=%d", n), func(b *testing.B) {
			var held uint64
			for b.Loop() {
				held = retainedBytes(func() any {
					keys := digestHexKeys("chunk/", n)
					set := make(map[[32]byte]struct{}, n)
					for _, key := range keys {
						digest, ok := decodeDigest("chunk/", key)
						if !ok {
							b.Fatal("key did not decode as a digest")
						}
						set[digest] = struct{}{}
					}
					return set
				})
			}
			b.ReportMetric(float64(held)/1e6, "MB-retained")
			b.ReportMetric(float64(held)/float64(n), "B/entry")
		})
	}
}
