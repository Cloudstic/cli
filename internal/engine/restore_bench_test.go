package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
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

// BenchmarkRestoreTraversal prices the two ways a restore can decide what to
// write next, over the same snapshot and against the same store.
//
// It is the measurement RFC 0025 §3 was missing, and the metric it reports is
// **retained** bytes rather than B/op. Allocation volume is the wrong question
// here and answers it backwards: the derived walk allocates rather more than
// the plan does, because it reads a batch, uses it and drops it, where the plan
// reads once and keeps everything. What separates them is what survives the
// traversal, so that is what is measured — heap in use once the traversal has
// finished, with whatever it produced still reachable.
//
// Peak RSS cannot see this at any size the harness runs: the plan is 2.8 MB at
// 5,000 files against ~192 MB of buffers a restore holds regardless, well inside
// the ±60 MB run-to-run spread.
//
// Neither variant writes anything: the emitter is in dry-run mode, so what is
// left is the traversal and the reads it makes.
func BenchmarkRestoreTraversal(b *testing.B) {
	for _, files := range []int{5000, 50000} {
		ctx := context.Background()
		src := NewMockSource()
		dirs := files / 100
		for d := range dirs {
			dir := fmt.Sprintf("dir-%d", d)
			src.Files[dir] = MockFile{Meta: core.FileMeta{FileID: dir, Name: dir, Type: core.FileTypeFolder}}
			for f := range 100 {
				id := fmt.Sprintf("%s/file-%d", dir, f)
				src.Files[id] = MockFile{
					Meta:    core.FileMeta{FileID: id, Name: fmt.Sprintf("file-%d", f), Parents: []string{dir}},
					Content: []byte(id),
				}
			}
		}
		dest := NewMockStore()
		if _, err := NewBackupManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()}, src).Run(ctx); err != nil {
			b.Fatalf("backup: %v", err)
		}

		newManager := func() (*RestoreManager, string) {
			rm := NewRestoreManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
			snap, _, err := rm.resolveSnapshot(ctx, "")
			if err != nil {
				b.Fatalf("resolveSnapshot: %v", err)
			}
			return rm, snap.Root
		}

		entries := dirs * 101

		b.Run(fmt.Sprintf("derived/files=%d", files), func(b *testing.B) {
			for b.Loop() {
				rm, root := newManager()
				out := rm.newEmitter(ctx, nil, restoreConfig{dryRun: true}, &RestoreResult{}, 0)
				w := &derivedWalk{tree: rm.tree, store: rm.store, metas: rm.metas, out: out}
				b.ReportMetric(float64(retainedBytes(func() any {
					if _, err := w.run(ctx, root); err != nil {
						b.Fatalf("derived walk: %v", err)
					}
					// The manager is held alive in both variants so that the HAMT
					// node cache, which either traversal fills, counts against both.
					return [2]any{rm, w}
				}))/float64(entries), "retained_B/entry")
			}
		})

		b.Run(fmt.Sprintf("materialised/files=%d", files), func(b *testing.B) {
			for b.Loop() {
				rm, root := newManager()
				b.ReportMetric(float64(retainedBytes(func() any {
					byID, walkOrder, _, err := rm.collectMetadata(ctx, root)
					if err != nil {
						b.Fatalf("collectMetadata: %v", err)
					}
					return [3]any{rm, byID, restoreOrder(byID, walkOrder)}
				}))/float64(entries), "retained_B/entry")
			}
		})
	}
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
