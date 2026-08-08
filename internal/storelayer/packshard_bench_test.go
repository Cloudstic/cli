package storelayer

import (
	"encoding/json"
	"fmt"
	"testing"
)

// benchShard renders a shard object holding n entries, as loadShardsLocked
// would read from the store.
func benchShard(b *testing.B, n int) []byte {
	b.Helper()

	entries := make(map[string]PackEntry, n)
	ref := "packs/" + fmt.Sprintf("%064x", 1)
	for i := 0; i < n; i++ {
		entries[fmt.Sprintf("filemeta/%064x", i)] = PackEntry{
			PackRef: ref, Offset: int64(i * 512), Length: 512,
		}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		b.Fatal(err)
	}
	return data
}

// mergePackIndex decodes a shard straight into the catalog. It runs once per
// shard on every operation that opens a repository, so its cost is paid before
// any work the user asked for begins.
func BenchmarkMergePackIndex(b *testing.B) {
	for _, n := range []int{1000, 20000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			data := benchShard(b, n)

			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				c := newPackCatalog()
				if _, err := mergePackIndex(data, c); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// sealShardFor renders a shard without materialising the pending entries into a
// map first, which is what keeps a flush from holding a second copy of a run's
// worth of entries (#456).
func BenchmarkSealShardFor(b *testing.B) {
	const n = 20000
	c := newPackCatalog()
	pending := make(map[string]struct{}, n)
	ref := "packs/" + fmt.Sprintf("%064x", 1)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("filemeta/%064x", i)
		c.Set(key, PackEntry{PackRef: ref, Offset: int64(i * 512), Length: 512})
		pending[key] = struct{}{}
	}
	s := &PackStore{}

	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := s.sealShardFor(c, pending); err != nil {
			b.Fatal(err)
		}
	}
}
