package hamt

// A HAMT node exists in two forms: the one this package stores under
// "node/<sha256>" and the one it works with in memory. Both live here, together
// with the conversion between them, so that the encoding and the invariant it
// serves cannot drift apart.
//
// The stored form is the repository format. Its field set, struct tags and
// omitempty behavior are what every released version has written, and
// core.ComputeJSONHash marshals fields in declaration order — so changing any of
// it changes every root hash in every repository. TestRootHashGolden pins the
// bytes against exactly that.
//
// These types are unexported, and nothing outside this package names them. A
// node's layout is how the tree keeps its promises about routing and sharing;
// the value it carries is opaque to it. Callers see Tree, Txn and NodeStore,
// which speak in keys, values and refs.

// nodeType is the "type" discriminator of a stored node. It is a distinct type
// only so the two legal values cannot be confused with an arbitrary string; it
// marshals as the JSON string it is.
type nodeType string

const (
	nodeTypeInternal nodeType = "internal"
	nodeTypeLeaf     nodeType = "leaf"
)

// storedNode is a HAMT node as serialized under "node/<sha256>".
type storedNode struct {
	Type     nodeType    `json:"type"` // "internal" or "leaf"
	Bitmap   uint32      `json:"bitmap,omitempty"`
	Children []string    `json:"children,omitempty"` // ["node/<sha256>", ...]
	Entries  []leafEntry `json:"entries,omitempty"`
}

// leafEntry is one entry of a leaf node, in both forms — it holds no child
// pointers, so the stored and in-memory representations coincide.
//
// The tree treats Value as opaque. The backup engine happens to store filemeta
// refs in it, which is the only reason the wire tag reads "filemeta": that tag
// is a fact about this encoding, not about the map the tree implements.
type leafEntry struct {
	Key     string `json:"key"`                // caller's entry key (the source file ID)
	PathKey string `json:"path_key,omitempty"` // routing key; falls back to SHA256(Key) if empty
	Value   string `json:"filemeta"`           // "filemeta/<sha256>"

	// payload is the entry's format-v3 body (see payload.go): nil on every v2
	// entry, set on entries written into a v3 tree. Unexported and so invisible
	// to the JSON encoding above — a v2 node's bytes cannot change by this
	// field existing. Copied by pointer: a payload is immutable once attached.
	payload *Payload
}

// approxSize is the entry's contribution to its leaf's encoded size, for the
// v3 byte-budget split rule.
func (e *leafEntry) approxSize() int {
	return len(e.Key) + len(e.PathKey) + len(e.Value) + 16 + e.payload.approxSize()
}

// node is the in-memory form of a HAMT node. It differs from storedNode in one
// way that matters: a child is a child, not a ref string, so a node can point at
// another node that has never been serialized.
type node struct {
	leaf     bool
	bitmap   uint32
	children []child     // internal nodes only, packed per bitmap
	entries  []leafEntry // leaf nodes only, sorted by Key
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
		c.entries = make([]leafEntry, len(n.entries))
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
func decodeNode(sn *storedNode) *node {
	n := &node{leaf: sn.Type == nodeTypeLeaf}
	if n.leaf {
		n.entries = sn.Entries
		return n
	}
	n.bitmap = sn.Bitmap
	n.children = make([]child, len(sn.Children))
	for i, ref := range sn.Children {
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
func encodeNode(n *node, childRefs []string) *storedNode {
	if n.leaf {
		return &storedNode{Type: nodeTypeLeaf, Entries: n.entries}
	}
	return &storedNode{Type: nodeTypeInternal, Bitmap: n.bitmap, Children: childRefs}
}
