package hamt

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

// affinityKey mirrors engine.AffinityKey, the routing policy ScanPrefix exists
// to make readable. It is duplicated rather than imported because the engine
// depends on this package and not the other way round.
func affinityKey(parentID, fileID string) string {
	return core.ComputeHash([]byte(parentID))[:4] + core.ComputeHash([]byte(fileID))[4:]
}

func buildAffinityTree(t *testing.T, dirs, perDir int) (*Tree, string, map[string][]string) {
	t.Helper()
	ctx := context.Background()
	tree := NewTree(storetest.NewMemStore())
	txn := tree.Edit("")

	want := map[string][]string{}
	for d := range dirs {
		parent := fmt.Sprintf("dir-%d", d)
		for f := range perDir {
			fileID := fmt.Sprintf("%s/file-%d", parent, f)
			if err := txn.Insert(ctx, affinityKey(parent, fileID), fileID, "filemeta/"+fileID); err != nil {
				t.Fatalf("insert: %v", err)
			}
			want[parent] = append(want[parent], fileID)
		}
	}
	root, err := txn.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return tree, root, want
}

func scanKeys(t *testing.T, tree *Tree, root, prefix string) []string {
	t.Helper()
	var got []string
	if err := tree.ScanPrefix(context.Background(), root, prefix, func(key, _ string) error {
		got = append(got, key)
		return nil
	}); err != nil {
		t.Fatalf("ScanPrefix(%q): %v", prefix, err)
	}
	sort.Strings(got)
	return got
}

// The property the derived traversal rests on: every entry routed under a
// parent's prefix is found by descending to that prefix, at every tree size,
// including the sizes where the tree is shallower than the prefix is wide.
func TestScanPrefix_FindsEveryEntryUnderAParent(t *testing.T) {
	for _, tc := range []struct{ dirs, perDir int }{
		{1, 1},    // one leaf at the root
		{4, 4},    // still a single leaf
		{40, 5},   // split, leaves above the prefix width
		{8, 200},  // one directory far wider than maxLeafSize
		{300, 12}, // deep enough for leaves below the prefix width
	} {
		t.Run(fmt.Sprintf("%ddirs_%dfiles", tc.dirs, tc.perDir), func(t *testing.T) {
			tree, root, want := buildAffinityTree(t, tc.dirs, tc.perDir)
			for parent, ids := range want {
				sort.Strings(ids)
				got := scanKeys(t, tree, root, core.ComputeHash([]byte(parent))[:4])

				// The scan may legitimately return a colliding directory's
				// entries too; what it may never do is miss one of its own.
				found := map[string]bool{}
				for _, k := range got {
					found[k] = true
				}
				for _, id := range ids {
					if !found[id] {
						t.Fatalf("parent %s: entry %s not returned by its own prefix scan", parent, id)
					}
				}
			}
		})
	}
}

// An empty prefix constrains nothing, so the scan is the walk.
func TestScanPrefix_EmptyPrefixIsWalk(t *testing.T) {
	tree, root, _ := buildAffinityTree(t, 20, 7)

	var walked []string
	if err := tree.Walk(context.Background(), root, func(key, _ string) error {
		walked = append(walked, key)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	var scanned []string
	if err := tree.ScanPrefix(context.Background(), root, "", func(key, _ string) error {
		scanned = append(scanned, key)
		return nil
	}); err != nil {
		t.Fatalf("ScanPrefix: %v", err)
	}

	if len(walked) != len(scanned) {
		t.Fatalf("scan returned %d entries, walk returned %d", len(scanned), len(walked))
	}
	for i := range walked {
		if walked[i] != scanned[i] {
			t.Fatalf("scan diverged from walk at %d: %q vs %q", i, scanned[i], walked[i])
		}
	}
}

// A prefix nothing routes under returns nothing rather than everything, which
// is the failure that would make a derived restore silently write the whole
// snapshot into one directory.
func TestScanPrefix_UnusedPrefixIsEmpty(t *testing.T) {
	tree, root, want := buildAffinityTree(t, 30, 6)

	used := map[string]bool{}
	for parent := range want {
		used[core.ComputeHash([]byte(parent))[:4]] = true
	}
	for i := range 4096 {
		p := fmt.Sprintf("%04x", i)
		if used[p] {
			continue
		}
		if got := scanKeys(t, tree, root, p); len(got) != 0 {
			t.Fatalf("prefix %s returned %d entries for a parent nothing routes under", p, len(got))
		}
	}
}

// The scan filters as well as prunes: entries sharing the path down to a leaf
// but not the full prefix belong to another parent.
func TestScanPrefix_RejectsNeighboursSharingTheDescent(t *testing.T) {
	ctx := context.Background()
	tree := NewTree(storetest.NewMemStore())
	txn := tree.Edit("")

	// Two routing keys agreeing on their first hex character and differing in
	// the second sit under the same level-0 bucket, and in the same leaf while
	// the tree is small.
	if err := txn.Insert(ctx, "ab000000", "wanted", "v1"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := txn.Insert(ctx, "af000000", "neighbour", "v2"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	root, err := txn.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	got := scanKeys(t, tree, root, "ab00")
	if len(got) != 1 || got[0] != "wanted" {
		t.Fatalf("scan returned %v, want only [wanted]", got)
	}
}

func TestScanPrefix_RejectsOversizedPrefix(t *testing.T) {
	tree, root, _ := buildAffinityTree(t, 2, 2)
	err := tree.ScanPrefix(context.Background(), root, "0123456789", func(string, string) error { return nil })
	if err == nil {
		t.Fatal("a prefix longer than the 32-bit routing window must be refused")
	}
}
