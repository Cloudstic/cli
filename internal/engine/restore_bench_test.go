package engine

import (
	"fmt"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// benchTree builds a snapshot's worth of metadata: dirs files each, in walk
// order, which is the order restore now writes leaves in.
func benchTree(dirs, perDir int) (map[string]core.FileMeta, []string) {
	byID := make(map[string]core.FileMeta, dirs*(perDir+1))
	walk := make([]string, 0, dirs*(perDir+1))

	for d := 0; d < dirs; d++ {
		dir := fmt.Sprintf("dir-%d", d)
		byID[dir] = core.FileMeta{FileID: dir, Type: core.FileTypeFolder}
		walk = append(walk, dir)
		for f := 0; f < perDir; f++ {
			id := fmt.Sprintf("%s/file-%d", dir, f)
			byID[id] = core.FileMeta{FileID: id, Parents: []string{dir}}
			walk = append(walk, id)
		}
	}
	return byID, walk
}

// restoreOrder sequences every entry in a snapshot before the first byte is
// written, so it is on the critical path of every restore.
func BenchmarkRestoreOrder(b *testing.B) {
	for _, files := range []int{5000, 50000} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			byID, walk := benchTree(files/100, 100)

			b.ReportAllocs()
			for b.Loop() {
				if got := restoreOrder(byID, walk); len(got) != len(byID) {
					b.Fatalf("ordered %d of %d", len(got), len(byID))
				}
			}
		})
	}
}
