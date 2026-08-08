package storelayer

import (
	"fmt"
	"runtime"
	"testing"
)

// benchCatalogKeys returns n realistic catalog keys: a packable namespace and a
// 64-character hex digest, which is the shape the compact encoding exists for.
func benchCatalogKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("filemeta/%064x", i)
	}
	return keys
}

func BenchmarkPackCatalogSet(b *testing.B) {
	keys := benchCatalogKeys(4096)
	entry := PackEntry{PackRef: "packs/" + fmt.Sprintf("%064x", 1), Offset: 4096, Length: 512}

	b.ReportAllocs()
	for b.Loop() {
		c := newPackCatalog()
		for _, k := range keys {
			c.Set(k, entry)
		}
	}
}

func BenchmarkPackCatalogGet(b *testing.B) {
	keys := benchCatalogKeys(4096)
	entry := PackEntry{PackRef: "packs/" + fmt.Sprintf("%064x", 1), Offset: 4096, Length: 512}
	c := newPackCatalog()
	for _, k := range keys {
		c.Set(k, entry)
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if _, ok := c.Get(keys[i%len(keys)]); !ok {
			b.Fatal("miss")
		}
		i++
	}
}

// BenchmarkPackCatalogBytesPerEntry reports what an entry costs to hold, which
// is the number #457 was about and the reason the catalog is not a
// map[string]PackEntry.
//
// Reported as a custom metric rather than read off B/op: B/op is allocation per
// iteration, and what matters here is what survives the fill. The two differ by
// whatever the map discarded while growing.
//
// Keys are built inside the measured region, not hoisted into a slice the two
// variants share. A map[string]PackEntry retains the key string it is given, so
// pre-allocating the keys hands it the 73 bytes of hex for free and makes the
// two representations look identical — which is exactly what the first version
// of this benchmark reported. In production those strings arrive from a JSON
// decode and nothing else holds them.
func BenchmarkPackCatalogBytesPerEntry(b *testing.B) {
	const n = 200000
	entry := PackEntry{PackRef: "packs/" + fmt.Sprintf("%064x", 1), Offset: 4096, Length: 512}

	for b.Loop() {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		c := newPackCatalog()
		for i := 0; i < n; i++ {
			c.Set(fmt.Sprintf("filemeta/%064x", i), entry)
		}

		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(n), "B/entry")
		runtime.KeepAlive(c)
	}
}

// BenchmarkPackCatalogBytesPerEntry_Map is the representation packCatalog
// replaced, kept so the comparison stays honest as both sides change.
func BenchmarkPackCatalogBytesPerEntry_Map(b *testing.B) {
	const n = 200000
	// Pack refs interned, as they were before the compact encoding, so this
	// measures the encoding rather than a saving already taken.
	ref := "packs/" + fmt.Sprintf("%064x", 1)

	for b.Loop() {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		m := make(map[string]PackEntry)
		for i := 0; i < n; i++ {
			m[fmt.Sprintf("filemeta/%064x", i)] = PackEntry{PackRef: ref, Offset: 4096, Length: 512}
		}

		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(n), "B/entry")
		runtime.KeepAlive(m)
	}
}
