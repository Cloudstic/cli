package hamt

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

// A tree's shape must be a function of its contents, not of the history that
// produced them. TestRootHashIsOrderIndependent pins that for insertion; these
// pin it for deletion, which is where it used to fail: dropping empty slots and
// promoting a lone remaining leaf left several under-full leaves in place, so a
// subtree that split under load and later shrank stayed split.
//
// The consequence was never incorrect results — lookup, walk and diff read a
// drifted tree the same as any other — but equal content reached by different
// routes produced different roots, which costs node-level deduplication between
// repositories and leaves shrinking trees carrying nodes they no longer need.

// treeFrom builds a tree containing exactly entries, inserted in one
// transaction, and returns its root.
func treeFrom(t *testing.T, tree *Tree, entries []op) string {
	t.Helper()
	ctx := context.Background()
	tx := tree.Edit("")
	for _, e := range entries {
		if err := tx.Insert(ctx, routingKey(e.parentID, e.fileID), e.fileID, e.value); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	root, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return root
}

// TestRootHashIsDeletionHistoryIndependent covers the sizes that matter: a
// survivor count above maxLeafSize must stay internal, and one at or below it
// must collapse back to exactly the tree a fresh build produces.
func TestRootHashIsDeletionHistoryIndependent(t *testing.T) {
	const grown = 400

	for _, keep := range []int{0, 1, 2, maxLeafSize - 1, maxLeafSize, maxLeafSize + 1, 100, grown} {
		t.Run(fmt.Sprintf("keep-%d", keep), func(t *testing.T) {
			var all []op
			for i := range grown {
				all = append(all, ins(
					fmt.Sprintf("dir-%02d", i%13),
					fmt.Sprintf("f%04d", i),
					fmt.Sprintf("filemeta/v%04d", i),
				))
			}
			survivors := all[:keep]

			// Grown: build everything, then delete down to the survivors.
			shrunk := scenario{name: "shrunk", ops: all}
			for i := keep; i < grown; i++ {
				shrunk.ops = append(shrunk.ops, del(all[i].parentID, all[i].fileID))
			}
			shrunkRoot := runScenario(t, shrunk)

			// Fresh: build only the survivors.
			freshStore := newInMemoryStore()
			freshTree := NewTree(freshStore)
			freshRoot := treeFrom(t, freshTree, survivors)

			if shrunkRoot != freshRoot {
				t.Fatalf("deleting down to %d entries produced %s, building them directly produced %s",
					keep, shrunkRoot, freshRoot)
			}
		})
	}
}

// Deleting a whole directory is the case affinity routing makes tidy — the
// entries share a routing prefix, so whole subtrees empty at once — and it
// already collapsed before this rule existed. It is pinned so that the general
// rule does not regress it.
func TestDeletingAWholeDirectoryStaysCanonical(t *testing.T) {
	var all []op
	for d := range 12 {
		for f := range 40 {
			all = append(all, ins(
				fmt.Sprintf("dir-%02d", d),
				fmt.Sprintf("dir-%02d/f%03d", d, f),
				fmt.Sprintf("filemeta/v%02d-%03d", d, f),
			))
		}
	}

	shrunk := scenario{name: "drop-half-the-dirs", ops: all}
	var survivors []op
	for _, e := range all {
		var d int
		if _, err := fmt.Sscanf(e.parentID, "dir-%02d", &d); err != nil {
			t.Fatalf("parse %q: %v", e.parentID, err)
		}
		if d < 6 {
			shrunk.ops = append(shrunk.ops, del(e.parentID, e.fileID))
		} else {
			survivors = append(survivors, e)
		}
	}

	shrunkRoot := runScenario(t, shrunk)
	freshRoot := treeFrom(t, NewTree(newInMemoryStore()), survivors)
	if shrunkRoot != freshRoot {
		t.Errorf("dropping whole directories drifted: %s vs a fresh build's %s", shrunkRoot, freshRoot)
	}
}

// The collapse must not read a subtree it cannot collapse. A tree far larger
// than a leaf is rejected after seeing just over maxLeafSize entries, so the
// check costs a couple of node loads rather than a full traversal — otherwise
// every delete in a large tree would pay for a walk of it.
func TestCollapseDoesNotTraverseLargeSubtrees(t *testing.T) {
	ctx := context.Background()
	counting := &countingNodeStore{inner: newInMemoryStore()}
	tree := NewTree(counting)

	var all []op
	for i := range 4000 {
		all = append(all, ins("", fmt.Sprintf("f%05d", i), fmt.Sprintf("filemeta/v%05d", i)))
	}
	root := treeFrom(t, tree, all)

	counting.reads = 0
	tx := tree.Edit(root)
	if err := tx.Delete(ctx, routingKey("", "f00000"), "f00000"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// A full traversal of a 4000-entry tree reads on the order of 150 nodes.
	// The budget keeps one delete to the spine it descends plus a shallow probe.
	if counting.reads > 40 {
		t.Errorf("one delete read %d nodes from a 4000-entry tree; the collapse check"+
			" is traversing subtrees it cannot collapse", counting.reads)
	}
	t.Logf("one delete in a 4000-entry tree read %d nodes", counting.reads)
}

// countingNodeStore counts node reads so a traversal budget can be asserted.
type countingNodeStore struct {
	inner *inMemoryStore
	reads int
}

func (c *countingNodeStore) Get(ctx context.Context, key string) ([]byte, error) {
	c.reads++
	return c.inner.Get(ctx, key)
}
func (c *countingNodeStore) Put(ctx context.Context, key string, data []byte) error {
	return c.inner.Put(ctx, key, data)
}
func (c *countingNodeStore) Exists(ctx context.Context, key string) (bool, error) {
	return c.inner.Exists(ctx, key)
}
func (c *countingNodeStore) Delete(ctx context.Context, key string) error {
	return c.inner.Delete(ctx, key)
}
func (c *countingNodeStore) List(ctx context.Context, prefix string) ([]string, error) {
	return c.inner.List(ctx, prefix)
}
func (c *countingNodeStore) Size(ctx context.Context, key string) (int64, error) {
	return c.inner.Size(ctx, key)
}
func (c *countingNodeStore) TotalSize(ctx context.Context) (int64, error) {
	return c.inner.TotalSize(ctx)
}
func (c *countingNodeStore) Flush(ctx context.Context) error { return c.inner.Flush(ctx) }

// Replacement is the third way a tree's shape can drift from its contents, and
// the one only format v3 exposes.
//
// v2 splits a leaf on entry count, which a replacement cannot change, so
// overwriting an entry there is shape-neutral by construction. v3 splits on
// encoded bytes and a v3 entry carries the file's own bytes, so replacing one
// moves a leaf's size by as much as inserting or deleting it does: a small file
// growing large overfills a leaf that never reconsiders splitting, and a large
// one shrinking leaves behind a subtree that split under content it no longer
// holds. Both produce a root that a fresh build of the same entries does not,
// which is what node-level deduplication between snapshots rests on.
//
// This reaches a real repository through the change-feed scans
// (gdrive-changes, onedrive-changes), which edit the previous snapshot's tree
// in place rather than rebuilding it, so every changed file is a replacement.

// v3EntriesWithOneSized builds n entries whose payloads are all smallBytes long
// except entry `at`, which is bigBytes long.
func v3EntriesWithOneSized(n, at, size int) []*Payload {
	out := make([]*Payload, n)
	for i := range out {
		body := 1024
		if i == at {
			body = size
		}
		out[i] = &Payload{
			Meta:   fmt.Appendf(nil, `{"fileId":"file-%d"}`, i),
			Size:   int64(body),
			Inline: bytes.Repeat([]byte{byte('a' + i%26)}, body),
		}
	}
	return out
}

// v3TreeFrom builds a v3 tree holding exactly payloads and returns its root.
func v3TreeFrom(t *testing.T, payloads []*Payload) string {
	t.Helper()
	tree, _ := v3TestTree()
	tx := tree.Edit("")
	for i, p := range payloads {
		key := fmt.Sprintf("file-%d", i)
		if err := tx.InsertWithPayload(ctx, routingKeyFor(i), key, fmt.Sprintf("filemeta/%064d", i), p); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	root, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return root
}

func TestV3RootHashIsReplacementHistoryIndependent(t *testing.T) {
	// Enough entries at a megabyte each that the set is several leaf budgets
	// wide, so the tree genuinely splits and there is a shape to get wrong.
	const n = 24
	const big = 1024 * 1024
	const target = 7

	t.Run("shrink", func(t *testing.T) {
		// One entry carries the whole set over the budget on its own, so the
		// tree splits to hold it and must collapse back to a single leaf once
		// it no longer does.
		start := v3EntriesWithOneSized(n, target, leafSplitBytesV3+big)
		tree, _ := v3TestTree()
		tx := tree.Edit("")
		for i, p := range start {
			if err := tx.InsertWithPayload(ctx, routingKeyFor(i), fmt.Sprintf("file-%d", i), fmt.Sprintf("filemeta/%064d", i), p); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		end := v3EntriesWithOneSized(n, -1, 0)
		if err := tx.InsertWithPayload(ctx, routingKeyFor(target), fmt.Sprintf("file-%d", target),
			fmt.Sprintf("filemeta/%064d", target), end[target]); err != nil {
			t.Fatalf("replace: %v", err)
		}
		got, err := tx.Commit(ctx)
		if err != nil {
			t.Fatalf("commit: %v", err)
		}

		if want := v3TreeFrom(t, end); got != want {
			t.Fatalf("shrinking an entry produced %s, building the same entries directly produced %s", got, want)
		}
	})

	t.Run("grow", func(t *testing.T) {
		// Built small, then overwritten big: a leaf pushed past the budget by a
		// replacement must split exactly as one pushed past it by an insert.
		start := v3EntriesWithOneSized(n, -1, 0)
		tree, _ := v3TestTree()
		tx := tree.Edit("")
		for i, p := range start {
			if err := tx.InsertWithPayload(ctx, routingKeyFor(i), fmt.Sprintf("file-%d", i), fmt.Sprintf("filemeta/%064d", i), p); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		end := v3EntriesWithOneSized(n, target, leafSplitBytesV3+big)
		if err := tx.InsertWithPayload(ctx, routingKeyFor(target), fmt.Sprintf("file-%d", target),
			fmt.Sprintf("filemeta/%064d", target), end[target]); err != nil {
			t.Fatalf("replace: %v", err)
		}
		got, err := tx.Commit(ctx)
		if err != nil {
			t.Fatalf("commit: %v", err)
		}

		if want := v3TreeFrom(t, end); got != want {
			t.Fatalf("growing an entry produced %s, building the same entries directly produced %s", got, want)
		}
	})
}
