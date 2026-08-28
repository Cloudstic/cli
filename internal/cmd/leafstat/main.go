// Command leafstat reports what a format-v3 repository's HAMT leaves are made
// of, and how much of the tree each snapshot rewrites.
//
// The other measurement tools observe a repository from outside: bench.sh times
// operations and counts requests, benchreport renders them. Neither can see
// *why* a v3 repository grows with retained snapshots, because the answer is a
// property of individual leaves — how large they are, how much of each is file
// content rather than metadata, and how many of them a churned backup has to
// rewrite. That question decided issue #525, and this is what answered it.
//
// Two modes:
//
//	leafstat -repo DIR [-snapshot REF] [-per-leaf]
//	leafstat -repo DIR [-snapshot REF] -refs
//
// The first summarises one snapshot's tree. The second prints "<ref> <bytes>"
// per node, which set-differencing across snapshots turns into the rewrite
// cost of each one:
//
//	comm -23 <(cut -d' ' -f1 snap5.txt | sort -u) <(sort -u earlier.txt)
//
// Local, unencrypted repositories only — it assembles the store chain itself
// rather than opening a client, so there is nowhere to put a key.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// leaf is one node's contribution, split the way the leaf-budget question
// needs it: metadata is what a changed entry actually changes, inline content
// is what a rewrite drags along with it.
type leaf struct {
	ref     string
	entries int
	meta    int
	inline  int
	chunks  int
	nchunks int
}

func (l leaf) size() int { return l.meta + l.inline + l.chunks }

func main() {
	repo := flag.String("repo", "", "path to a local, unencrypted v3 repository")
	snap := flag.String("snapshot", "", "snapshot ref to read (default: the latest)")
	perLeaf := flag.Bool("per-leaf", false, "print one line per leaf, smallest first")
	refsOnly := flag.Bool("refs", false, `print "<node ref> <encoded bytes>" per node and exit`)
	flag.Parse()

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "leafstat: -repo is required")
		os.Exit(2)
	}

	ctx := context.Background()
	backend, err := local.New(*repo)
	check(err)
	// The chain a v3 repository without encryption is written through. v3 has
	// no pack layer, and nodes still pass through compression on the way out.
	s := storelayer.NewCompressedStore(backend)

	root, err := rootOf(ctx, s, *snap)
	check(err)

	nodes, err := collect(ctx, s, root)
	check(err)

	if *refsOnly {
		for _, n := range nodes {
			fmt.Printf("%s %d\n", n.ref, n.size())
		}
		return
	}
	report(root, nodes, *perLeaf)
}

// collect walks the tree once, attributing each entry to the node that
// delivered it. WalkTree is depth-first and sequential, so every entry between
// two onNode calls belongs to the first of them.
func collect(ctx context.Context, s store.ObjectStore, root string) ([]leaf, error) {
	var nodes []leaf
	var cur *leaf
	err := hamt.NewTree(s, hamt.WithFormatV3()).WalkTree(ctx, root,
		func(ref string) error {
			nodes = append(nodes, leaf{ref: ref})
			cur = &nodes[len(nodes)-1]
			return nil
		},
		func(_, _ string, p *hamt.Payload) error {
			cur.entries++
			if p == nil {
				return nil
			}
			cur.meta += len(p.Meta)
			cur.inline += len(p.Inline)
			cur.nchunks += len(p.Chunks)
			for _, c := range p.Chunks {
				cur.chunks += len(c) + 2
			}
			return nil
		})
	return nodes, err
}

func report(root string, nodes []leaf, perLeaf bool) {
	var leaves []leaf
	var internal int
	for _, n := range nodes {
		if n.entries == 0 {
			internal++
			continue
		}
		leaves = append(leaves, n)
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].size() < leaves[j].size() })

	var meta, inline, chunks, entries, metaOnly, metaOnlyBytes int
	for _, l := range leaves {
		meta, inline, chunks, entries = meta+l.meta, inline+l.inline, chunks+l.chunks, entries+l.entries
		if l.inline == 0 {
			metaOnly++
			metaOnlyBytes += l.size()
		}
	}
	total := meta + inline + chunks

	fmt.Printf("root %s\n", root)
	fmt.Printf("nodes: %d internal, %d leaves, %d entries\n", internal, len(leaves), entries)
	fmt.Printf("encoded: %s total  meta %s (%.0f%%)  inline %s (%.0f%%)  chunk refs %s (%.0f%%)\n",
		size(total), size(meta), pct(meta, total), size(inline), pct(inline, total), size(chunks), pct(chunks, total))
	if len(leaves) > 0 {
		fmt.Printf("leaf size: min %s  p50 %s  p90 %s  max %s  mean %s\n",
			size(leaves[0].size()), size(leaves[len(leaves)/2].size()),
			size(leaves[(len(leaves)*9)/10].size()), size(leaves[len(leaves)-1].size()),
			size(total/len(leaves)))
	}
	fmt.Printf("metadata-only leaves (no inline content): %d of %d, %s\n",
		metaOnly, len(leaves), size(metaOnlyBytes))

	if !perLeaf {
		return
	}
	for _, l := range leaves {
		fmt.Printf("  %s entries=%d size=%s meta=%s inline=%s chunkrefs=%s(%d)\n",
			short(l.ref), l.entries, size(l.size()), size(l.meta), size(l.inline), size(l.chunks), l.nchunks)
	}
}

// rootOf resolves a snapshot ref to its tree root, defaulting to whatever
// index/latest points at.
func rootOf(ctx context.Context, s store.ObjectStore, ref string) (string, error) {
	if ref == "" {
		data, err := s.Get(ctx, "index/latest")
		if err != nil {
			return "", err
		}
		var latest struct {
			Snapshot string `json:"latest_snapshot"`
		}
		if err := json.Unmarshal(data, &latest); err != nil {
			return "", fmt.Errorf("decode index/latest: %w", err)
		}
		ref = latest.Snapshot
	}
	data, err := s.Get(ctx, ref)
	if err != nil {
		return "", err
	}
	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return "", fmt.Errorf("decode %s: %w", ref, err)
	}
	return snap.Root, nil
}

func short(ref string) string {
	if len(ref) < 13 {
		return ref
	}
	return ref[5:13]
}

func size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "leafstat:", err)
		os.Exit(1)
	}
}
