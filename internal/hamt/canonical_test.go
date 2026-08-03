package hamt

import (
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
