package objkey

import (
	"fmt"
	"runtime"
	"testing"
)

func benchKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("chunk/%064x", i)
	}
	return keys
}

func BenchmarkSetAdd(b *testing.B) {
	keys := benchKeys(4096)

	b.ReportAllocs()
	for b.Loop() {
		s := NewSet()
		for _, k := range keys {
			s.Add(k)
		}
	}
}

func BenchmarkSetHas(b *testing.B) {
	keys := benchKeys(4096)
	s := NewSet()
	for _, k := range keys {
		s.Add(k)
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if !s.Has(keys[i%len(keys)]) {
			b.Fatal("miss")
		}
		i++
	}
}

// BenchmarkSetBytesPerEntry reports what one object costs to hold in prune's
// reachable set and check's verified set — the number this representation
// exists for, since both structures are sized by the repository rather than by
// the work in front of them.
//
// Reported as a custom metric rather than read off B/op: B/op is allocation per
// iteration, and what matters here is what survives the fill. The two differ by
// whatever the map discarded while growing.
//
// Keys are built inside the measured region, not hoisted into a slice the two
// variants share. A map[string]bool retains the key string it is given, so
// pre-allocating the keys hands it the 73 bytes of hex for free and makes the
// two representations look identical. In production those strings arrive from a
// store listing or a decoded content manifest, and nothing else holds them.
func BenchmarkSetBytesPerEntry(b *testing.B) {
	const n = 200000

	for b.Loop() {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		s := NewSet()
		for i := range n {
			s.Add(fmt.Sprintf("chunk/%064x", i))
		}

		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(n), "B/entry")
		runtime.KeepAlive(s)
	}
}

// BenchmarkSetBytesPerEntry_Map is the representation Set replaced, kept so the
// comparison stays honest as both sides change.
func BenchmarkSetBytesPerEntry_Map(b *testing.B) {
	const n = 200000

	for b.Loop() {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		m := make(map[string]bool)
		for i := range n {
			m[fmt.Sprintf("chunk/%064x", i)] = true
		}

		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(n), "B/entry")
		runtime.KeepAlive(m)
	}
}
