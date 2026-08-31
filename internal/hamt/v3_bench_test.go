package hamt

import (
	"fmt"
	"testing"
)

// BenchmarkV3RebuildTree is the shape of a full scan's transaction: every
// entry of the source is inserted into a tree opened on nothing, in an order
// unrelated to the routing key, and the result is sealed.
//
// It exists because that loop is what a no-change backup spends its time in
// (issue #538) and none of the existing benchmarks reach it: the affinity
// benchmarks build v2 trees of 1,000 payload-free entries, which neither
// splits on bytes nor fills a leaf far enough for the per-insert scans to
// show. Entry counts bracket the leaf entry cap, since the per-insert cost is
// a function of how full a leaf is rather than of how many there are.
func BenchmarkV3RebuildTree(b *testing.B) {
	for _, n := range []int{1000, 8000, 25000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			keys := make([]string, n)
			values := make([]string, n)
			payloads := make([]*Payload, n)
			for i := range n {
				keys[i] = routingKeyFor(i)
				values[i] = fmt.Sprintf("filemeta/%064d", i)
				payloads[i] = testPayload(i)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				tree, _ := v3TestTree()
				tx := tree.Edit("")
				for i := range n {
					if err := tx.InsertWithPayload(ctx, keys[i], fmt.Sprintf("file-%d", i), values[i], payloads[i]); err != nil {
						b.Fatalf("insert %d: %v", i, err)
					}
				}
			}
		})
	}
}
