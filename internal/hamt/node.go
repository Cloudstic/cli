package hamt

import "github.com/cloudstic/cli/internal/core"

// node is the in-memory form of a HAMT node. It differs from core.HAMTNode in
// one way that matters: a child is a child, not a ref string, so a node can
// point at another node that has never been serialized.
type node struct {
	leaf     bool
	bitmap   uint32
	children []child          // internal nodes only, packed per bitmap
	entries  []core.LeafEntry // leaf nodes only, sorted by Key
}

// child is one slot in an internal node: either clean, meaning it is already
// persisted and identified by ref, or dirty, meaning it exists only in memory
// and has no ref until the tree is sealed.
//
// The distinction is what lets a mutation stay in memory. A clean child is
// shared — it may be reachable from an older snapshot, and NodeStore may have
// handed the same pointer to someone else — so writing to it requires copying
// it first (see Txn.own). A dirty child was created by this transaction, is
// reachable from exactly one slot, and can be mutated in place.
type child struct {
	ref  string
	node *node
}

func (c child) isZero() bool { return c.ref == "" && c.node == nil }

// clone returns a dirty copy of n, safe to mutate. The slices are copied
// because the original may be a cached clean node shared with other readers;
// the elements are values or refs, so a shallow copy is enough.
func (n *node) clone() *node {
	c := &node{leaf: n.leaf, bitmap: n.bitmap}
	if n.children != nil {
		c.children = make([]child, len(n.children))
		copy(c.children, n.children)
	}
	if n.entries != nil {
		c.entries = make([]core.LeafEntry, len(n.entries))
		copy(c.entries, n.entries)
	}
	return c
}

// indexOfKey returns the position of key in a leaf's entries, or -1.
func (n *node) indexOfKey(key string) int {
	for i, e := range n.entries {
		if e.Key == key {
			return i
		}
	}
	return -1
}

// decodeNode converts the on-disk form into the in-memory form. Every child of
// a decoded node is clean by definition.
func decodeNode(hn *core.HAMTNode) *node {
	n := &node{leaf: hn.Type == core.ObjectTypeLeaf}
	if n.leaf {
		n.entries = hn.Entries
		return n
	}
	n.bitmap = hn.Bitmap
	n.children = make([]child, len(hn.Children))
	for i, ref := range hn.Children {
		n.children[i] = child{ref: ref}
	}
	return n
}

// encodeNode converts back to the on-disk form. childRefs must already hold the
// resolved ref of every child, in order; the caller seals children first so
// that this is a pure function of already-committed state.
//
// The shape here is the repository format: same field set, same struct tags,
// same omitempty behavior as every release before this one. Changing it changes
// every root hash.
func encodeNode(n *node, childRefs []string) *core.HAMTNode {
	if n.leaf {
		return &core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: n.entries}
	}
	return &core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: n.bitmap, Children: childRefs}
}
