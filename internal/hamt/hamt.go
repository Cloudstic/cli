package hamt

import (
	"context"
	"fmt"
	"io"
	"math/bits"
	"slices"
	"sort"
	"strconv"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

const (
	bitsPerLevel = 5
	branching    = 32 // 2^bitsPerLevel
	maxDepth     = 6
	maxLeafSize  = 32

	nodePrefix = "node/"

	// maxTreeDepth bounds every recursive descent. Routing consumes 5 bits per
	// level out of a 32-bit prefix, so a well-formed tree cannot nest deeper
	// than maxDepth internal levels plus its leaves. Going past it means the
	// structure is not a tree: a child ref reaching back into its own ancestry
	// would otherwise recurse until the stack runs out. Hash verification in
	// NodeStore.load makes such a cycle hard to build, but this guard costs one
	// comparison and turns a hang into an error.
	maxTreeDepth = maxDepth + 2
)

// errTooDeep reports a descent past maxTreeDepth.
func errTooDeep(depth int) error {
	return fmt.Errorf("hamt node nesting exceeds %d levels at depth %d: tree is cyclic or corrupt", maxTreeDepth, depth)
}

// DiffEntry represents a single change between two trees.
// OldValue is empty for additions; NewValue is empty for deletions.
type DiffEntry struct {
	Key      string
	OldValue string
	NewValue string
}

// Tree reads persistent Hash Array Mapped Tries.
//
// A HAMT here is a map from (routing key, key) to an opaque string value. The
// routing key is a hex string chosen by the caller; its first 32 bits decide
// the path through the trie, so callers wanting locality between related keys
// give them a shared prefix. The key identifies the entry within its leaf. The
// value is opaque — callers store object refs there, but the tree never
// interprets them.
//
// Tree itself holds no tree state: every read names the root it applies to, so
// one Tree serves any number of snapshots and shares a single node cache
// between them. Mutation happens in a Txn, obtained from Edit.
type Tree struct {
	nodes *NodeStore
}

// NewTree creates a Tree backed by the given object store.
func NewTree(s store.ObjectStore, opts ...TreeOption) *Tree {
	ns := NewNodeStore(s)
	for _, opt := range opts {
		opt(ns)
	}
	return &Tree{nodes: ns}
}

// TreeOption configures the node store behind a Tree.
//
// It is variadic so that the trees which never log — diff, restore, find, ls,
// prune, check — keep their existing call sites unchanged.
type TreeOption func(*NodeStore)

// WithLogger sends the node store's debug output to w.
func WithLogger(w io.Writer) TreeOption {
	return func(ns *NodeStore) { ns.log = ns.log.To(w) }
}

// NewTreeWithNodes creates a Tree over an existing NodeStore, so several trees
// can share one read cache.
func NewTreeWithNodes(ns *NodeStore) *Tree {
	return &Tree{nodes: ns}
}

// errFoundSentinel is used to short-circuit a Walk in LookupByKey.
var errFoundSentinel = fmt.Errorf("found")

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// Lookup returns the value associated with key in the tree rooted at root, or
// ("", nil) if not found. routingKey must be the key the entry was inserted
// under.
func (t *Tree) Lookup(ctx context.Context, root, routingKey, key string) (string, error) {
	if root == "" {
		return "", nil
	}
	return lookup(ctx, t.nodes, child{ref: root}, routingKey, key)
}

// LookupByKey finds a value by walking the entire tree and matching on the raw
// key. This is O(N) and slower than Lookup, but does not require the entry's
// routing key. Use only when the routing key cannot be reconstructed.
func (t *Tree) LookupByKey(ctx context.Context, root, key string) (string, error) {
	if root == "" {
		return "", nil
	}
	return lookupByKey(ctx, t.nodes, child{ref: root}, key)
}

// Walk visits every (key, value) pair stored in the tree rooted at root.
func (t *Tree) Walk(ctx context.Context, root string, fn func(key, value string) error) error {
	if root == "" {
		return nil
	}
	return walk(ctx, t.nodes, child{ref: root}, 0, fn)
}

// Diff structurally compares two persisted trees and calls fn for every entry
// added, removed, or modified between root1 and root2.
func (t *Tree) Diff(ctx context.Context, root1, root2 string, fn func(DiffEntry) error) error {
	if root1 == root2 {
		return nil
	}
	return diff(ctx, t.nodes, child{ref: root1}, child{ref: root2}, fn)
}

// NodeRefs visits every HAMT node ref reachable from root (including root
// itself). This is useful for garbage-collection marking, and applies only to
// persisted trees: an uncommitted node has no ref.
func (t *Tree) NodeRefs(ctx context.Context, root string, fn func(ref string) error) error {
	if root == "" {
		return nil
	}
	return nodeRefs(ctx, t.nodes, root, 0, fn)
}

// ---------------------------------------------------------------------------
// Mutation
// ---------------------------------------------------------------------------

// Txn is a mutable working copy of a tree.
//
// Insert and Delete rewrite nodes in memory and write nothing; the resulting
// tree is a mixture of clean children, still shared with the tree it was opened
// from, and dirty ones that exist only here. Commit is the only write path: it
// serializes the dirty spine bottom-up and returns the new root ref. Nodes that
// were superseded during the transaction were never serialized in the first
// place, so there is nothing to garbage-collect afterwards.
//
// A Txn is not safe for concurrent use.
type Txn struct {
	nodes *NodeStore
	root  child
}

// Edit opens a transaction over the tree rooted at root. Pass an empty root to
// build a new tree.
func (t *Tree) Edit(root string) *Txn {
	tx := &Txn{nodes: t.nodes}
	if root != "" {
		tx.root = child{ref: root}
	}
	return tx
}

// Insert adds or updates the entry for key. routingKey decides the entry's path
// through the trie; key is stored as the leaf key.
func (tx *Txn) Insert(ctx context.Context, routingKey, key, value string) error {
	r, err := newRouting(routingKey)
	if err != nil {
		return err
	}
	c, err := tx.insert(ctx, tx.root, r, key, value, 0)
	if err != nil {
		return err
	}
	tx.root = c
	return nil
}

// Delete removes the entry for key. Deleting a key that is not present, or
// deleting from an empty tree, is a no-op.
func (tx *Txn) Delete(ctx context.Context, routingKey, key string) error {
	if tx.root.isZero() {
		return nil
	}
	r, err := newRouting(routingKey)
	if err != nil {
		return err
	}
	c, _, err := tx.delete(ctx, tx.root, r, key, 0)
	if err != nil {
		return err
	}
	tx.root = c
	return nil
}

// Lookup returns the value for key in the working tree, including changes made
// in this transaction that have not been committed.
func (tx *Txn) Lookup(ctx context.Context, routingKey, key string) (string, error) {
	if tx.root.isZero() {
		return "", nil
	}
	return lookup(ctx, tx.nodes, tx.root, routingKey, key)
}

// LookupByKey is Tree.LookupByKey over the working tree.
func (tx *Txn) LookupByKey(ctx context.Context, key string) (string, error) {
	if tx.root.isZero() {
		return "", nil
	}
	return lookupByKey(ctx, tx.nodes, tx.root, key)
}

// Walk visits every (key, value) pair in the working tree.
func (tx *Txn) Walk(ctx context.Context, fn func(key, value string) error) error {
	if tx.root.isZero() {
		return nil
	}
	return walk(ctx, tx.nodes, tx.root, 0, fn)
}

// DiffFrom compares a persisted tree against the working tree, reporting what
// this transaction would change. It writes nothing, so callers can report a
// would-be result without committing.
func (tx *Txn) DiffFrom(ctx context.Context, oldRoot string, fn func(DiffEntry) error) error {
	return diff(ctx, tx.nodes, child{ref: oldRoot}, tx.root, fn)
}

// Root computes the ref this tree would have if committed, without writing
// anything. Callers that only need to know whether a transaction changed
// anything — or that are reporting a dry run — use this instead of Commit.
func (tx *Txn) Root(ctx context.Context) (string, error) {
	ref, _, err := tx.seal()
	return ref, err
}

// Commit writes every node that is actually part of the final tree and returns
// the new root ref. Superseded intermediate nodes were never serialized, so
// "dirty" and "reachable" are the same set and no reachability pass is needed.
//
// After Commit the transaction is clean and can be committed again cheaply; a
// second Commit writes nothing and returns the same ref.
func (tx *Txn) Commit(ctx context.Context) (string, error) {
	ref, batch, err := tx.seal()
	if err != nil {
		return "", err
	}
	if err := tx.nodes.putAll(ctx, batch); err != nil {
		return "", err
	}
	// Every node is persisted now, so the whole tree is clean again.
	if ref == "" {
		tx.root = child{}
	} else {
		tx.root = child{ref: ref}
	}
	return ref, nil
}

// seal serializes the dirty spine bottom-up, returning the root ref and the
// nodes that would have to be written for that ref to resolve. It performs no
// I/O: Root discards the batch, Commit writes it.
func (tx *Txn) seal() (string, map[string][]byte, error) {
	batch := make(map[string][]byte)
	ref, err := tx.sealChild(tx.root, batch)
	if err != nil {
		return "", nil, err
	}
	return ref, batch, nil
}

func (tx *Txn) sealChild(c child, batch map[string][]byte) (string, error) {
	if c.node == nil {
		return c.ref, nil // clean (or absent): already persisted
	}

	var childRefs []string
	if !c.node.leaf {
		childRefs = make([]string, len(c.node.children))
		for i, cc := range c.node.children {
			ref, err := tx.sealChild(cc, batch)
			if err != nil {
				return "", err
			}
			childRefs[i] = ref
		}
	}

	ref, data, err := tx.nodes.seal(encodeNode(c.node, childRefs))
	if err != nil {
		return "", err
	}
	batch[ref] = data
	return ref, nil
}

// own returns a node for c that this transaction may mutate in place. A dirty
// child is already ours; a clean one is shared with the node cache and possibly
// with older snapshots, so it is copied first. This copy is the entire cost of
// a mutation — no marshalling, no hashing, no I/O.
func (tx *Txn) own(ctx context.Context, c child) (*node, error) {
	if c.node != nil {
		return c.node, nil
	}
	n, err := tx.nodes.load(ctx, c.ref)
	if err != nil {
		return nil, fmt.Errorf("load node %s: %w", c.ref, err)
	}
	return n.clone(), nil
}

// ---------------------------------------------------------------------------
// Internal: insert
// ---------------------------------------------------------------------------

func (tx *Txn) insert(ctx context.Context, c child, r routing, key, value string, level int) (child, error) {
	var n *node
	if c.isZero() {
		n = &node{leaf: true}
	} else {
		var err error
		if n, err = tx.own(ctx, c); err != nil {
			return child{}, err
		}
	}

	if n.leaf {
		return tx.insertIntoLeaf(n, r, key, value, level)
	}

	idx, err := r.indexAt(level)
	if err != nil {
		return child{}, err
	}
	pos, exists := childPos(n.bitmap, idx)

	var cur child
	if exists {
		cur = n.children[pos]
	}
	newChild, err := tx.insert(ctx, cur, r, key, value, level+1)
	if err != nil {
		return child{}, err
	}

	if exists {
		n.children[pos] = newChild
	} else {
		n.bitmap |= uint32(1) << idx
		n.children = slices.Insert(n.children, pos, newChild)
	}
	return child{node: n}, nil
}

func (tx *Txn) insertIntoLeaf(n *node, r routing, key, value string, level int) (child, error) {
	entry := leafEntry{Key: key, PathKey: r.hex, Value: value}

	if i := n.indexOfKey(key); i >= 0 {
		n.entries[i] = entry
		return child{node: n}, nil
	}

	// Append if the leaf has room, or if we can no longer split.
	if len(n.entries) < maxLeafSize || level >= maxDepth {
		n.entries = append(n.entries, entry)
		sortEntries(n.entries)
		return child{node: n}, nil
	}

	// Leaf full: split into an internal node.
	all := make([]leafEntry, len(n.entries), len(n.entries)+1)
	copy(all, n.entries)
	return buildNode(append(all, entry), level)
}

// buildNode recursively partitions entries into a subtree starting at level.
func buildNode(entries []leafEntry, level int) (child, error) {
	if len(entries) <= maxLeafSize || level >= maxDepth {
		sortEntries(entries)
		return child{node: &node{leaf: true, entries: entries}}, nil
	}

	buckets := make(map[int][]leafEntry)
	for _, e := range entries {
		r, err := routingForEntry(e)
		if err != nil {
			return child{}, err
		}
		idx, err := r.indexAt(level)
		if err != nil {
			return child{}, err
		}
		buckets[idx] = append(buckets[idx], e)
	}

	n := &node{}
	for i := 0; i < branching; i++ {
		bucket, ok := buckets[i]
		if !ok {
			continue
		}
		c, err := buildNode(bucket, level+1)
		if err != nil {
			return child{}, err
		}
		n.bitmap |= 1 << i
		n.children = append(n.children, c)
	}
	return child{node: n}, nil
}

// ---------------------------------------------------------------------------
// Internal: delete
// ---------------------------------------------------------------------------

// delete returns the child that should replace c, and whether anything changed.
// A zero child means the subtree became empty. When nothing changed, c is
// returned untouched — in particular a clean child stays clean, so a delete
// that misses costs no copy and no write.
func (tx *Txn) delete(ctx context.Context, c child, r routing, key string, level int) (child, bool, error) {
	n, err := tx.resolve(ctx, c)
	if err != nil {
		return c, false, err
	}

	if n.leaf {
		i := n.indexOfKey(key)
		if i < 0 {
			return c, false, nil
		}
		if len(n.entries) == 1 {
			return child{}, true, nil
		}
		owned, err := tx.own(ctx, c)
		if err != nil {
			return c, false, err
		}
		owned.entries = slices.Delete(owned.entries, i, i+1)
		return child{node: owned}, true, nil
	}

	idx, err := r.indexAt(level)
	if err != nil {
		return c, false, err
	}
	pos, exists := childPos(n.bitmap, idx)
	if !exists {
		return c, false, nil
	}

	newChild, changed, err := tx.delete(ctx, n.children[pos], r, key, level+1)
	if err != nil {
		return c, false, err
	}
	if !changed {
		return c, false, nil
	}

	owned, err := tx.own(ctx, c)
	if err != nil {
		return c, false, err
	}

	if !newChild.isZero() {
		owned.children[pos] = newChild
		return child{node: owned}, true, nil
	}

	// Child became empty; drop the slot.
	owned.bitmap &^= uint32(1) << idx
	if owned.bitmap == 0 {
		return child{}, true, nil
	}
	owned.children = slices.Delete(owned.children, pos, pos+1)

	// Collapse: if a single leaf remains, promote it in place of this node.
	if len(owned.children) == 1 {
		if only, err := tx.resolve(ctx, owned.children[0]); err == nil && only.leaf {
			return owned.children[0], true, nil
		}
	}
	return child{node: owned}, true, nil
}

// ---------------------------------------------------------------------------
// Internal: traversal
//
// These operate on a child rather than a ref, so the same code serves a
// persisted tree and a transaction's uncommitted working tree.
// ---------------------------------------------------------------------------

// resolve returns the node c points at, loading it if c is clean. The result
// must be treated as read-only; use Txn.own to obtain a mutable node.
func (tx *Txn) resolve(ctx context.Context, c child) (*node, error) {
	return resolve(ctx, tx.nodes, c)
}

func resolve(ctx context.Context, ns *NodeStore, c child) (*node, error) {
	if c.node != nil {
		return c.node, nil
	}
	return ns.load(ctx, c.ref)
}

func lookup(ctx context.Context, ns *NodeStore, c child, routingKey, key string) (string, error) {
	r, err := newRouting(routingKey)
	if err != nil {
		return "", err
	}
	for level := 0; level <= maxTreeDepth; level++ {
		n, err := resolve(ctx, ns, c)
		if err != nil {
			return "", err
		}
		if n.leaf {
			if i := n.indexOfKey(key); i >= 0 {
				return n.entries[i].Value, nil
			}
			return "", nil
		}

		idx, err := r.indexAt(level)
		if err != nil {
			return "", err
		}
		pos, exists := childPos(n.bitmap, idx)
		if !exists {
			return "", nil
		}
		if pos >= len(n.children) {
			return "", fmt.Errorf("corrupt node: bitmap indicates child but array too short")
		}
		c = n.children[pos]
	}
	return "", errTooDeep(maxTreeDepth)
}

func lookupByKey(ctx context.Context, ns *NodeStore, root child, key string) (string, error) {
	var found string
	err := walk(ctx, ns, root, 0, func(k, value string) error {
		if k == key {
			found = value
			return errFoundSentinel
		}
		return nil
	})
	if err == errFoundSentinel {
		return found, nil
	}
	return "", err
}

func walk(ctx context.Context, ns *NodeStore, c child, depth int, fn func(key, value string) error) error {
	if depth > maxTreeDepth {
		return errTooDeep(depth)
	}
	n, err := resolve(ctx, ns, c)
	if err != nil {
		return err
	}
	if n.leaf {
		for _, e := range n.entries {
			if err := fn(e.Key, e.Value); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cc := range n.children {
		if err := walk(ctx, ns, cc, depth+1, fn); err != nil {
			return err
		}
	}
	return nil
}

func nodeRefs(ctx context.Context, ns *NodeStore, ref string, depth int, fn func(string) error) error {
	if depth > maxTreeDepth {
		return errTooDeep(depth)
	}
	if err := fn(ref); err != nil {
		return err
	}
	n, err := ns.load(ctx, ref)
	if err != nil {
		return err
	}
	if n.leaf {
		return nil
	}
	for _, cc := range n.children {
		if err := nodeRefs(ctx, ns, cc.ref, depth+1, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: diff
// ---------------------------------------------------------------------------

func diff(ctx context.Context, ns *NodeStore, c1, c2 child, fn func(DiffEntry) error) error {
	// Identical clean subtrees have identical refs, which is what makes an
	// unchanged subtree free to skip.
	if c1.node == nil && c2.node == nil && c1.ref == c2.ref {
		return nil
	}
	n1, err := resolve(ctx, ns, c1)
	if err != nil {
		return err
	}
	n2, err := resolve(ctx, ns, c2)
	if err != nil {
		return err
	}
	return diffNodes(ctx, ns, n1, n2, 0, fn)
}

func diffNodes(ctx context.Context, ns *NodeStore, n1, n2 *node, level int, fn func(DiffEntry) error) error {
	if level > maxTreeDepth {
		return errTooDeep(level)
	}
	if n1.leaf && n2.leaf {
		return diffLeaves(n1, n2, fn)
	}

	for i := 0; i < branching; i++ {
		c1, err := childForBucket(ctx, ns, n1, i, level)
		if err != nil {
			return err
		}
		c2, err := childForBucket(ctx, ns, n2, i, level)
		if err != nil {
			return err
		}

		switch {
		case c1 == nil && c2 == nil:
			continue
		case c1 == nil:
			if err := collectAll(ctx, ns, c2, level, func(k, v string) error { return fn(DiffEntry{Key: k, NewValue: v}) }); err != nil {
				return err
			}
		case c2 == nil:
			if err := collectAll(ctx, ns, c1, level, func(k, v string) error { return fn(DiffEntry{Key: k, OldValue: v}) }); err != nil {
				return err
			}
		default:
			if err := diffNodes(ctx, ns, c1, c2, level+1, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// childForBucket returns the child node for bucket idx.
// When the node is a leaf acting as a virtual internal (mixed-type comparison),
// it returns a synthetic leaf containing only entries that route to this bucket.
func childForBucket(ctx context.Context, ns *NodeStore, n *node, idx, level int) (*node, error) {
	if !n.leaf {
		pos, exists := childPos(n.bitmap, idx)
		if !exists {
			return nil, nil
		}
		return resolve(ctx, ns, n.children[pos])
	}

	var filtered []leafEntry
	for _, e := range n.entries {
		r, err := routingForEntry(e)
		if err != nil {
			continue
		}
		i, err := r.indexAt(level)
		if err != nil {
			continue
		}
		if i == idx {
			filtered = append(filtered, e)
		}
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	return &node{leaf: true, entries: filtered}, nil
}

func diffLeaves(n1, n2 *node, fn func(DiffEntry) error) error {
	left := make(map[string]string, len(n1.entries))
	for _, e := range n1.entries {
		left[e.Key] = e.Value
	}

	for _, e := range n2.entries {
		old, ok := left[e.Key]
		if !ok {
			if err := fn(DiffEntry{Key: e.Key, NewValue: e.Value}); err != nil {
				return err
			}
			continue
		}
		if old != e.Value {
			if err := fn(DiffEntry{Key: e.Key, OldValue: old, NewValue: e.Value}); err != nil {
				return err
			}
		}
		delete(left, e.Key)
	}

	for k, v := range left {
		if err := fn(DiffEntry{Key: k, OldValue: v}); err != nil {
			return err
		}
	}
	return nil
}

func collectAll(ctx context.Context, ns *NodeStore, n *node, depth int, fn func(key, value string) error) error {
	if depth > maxTreeDepth {
		return errTooDeep(depth)
	}
	if n.leaf {
		for _, e := range n.entries {
			if err := fn(e.Key, e.Value); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cc := range n.children {
		child, err := resolve(ctx, ns, cc)
		if err != nil {
			return err
		}
		if err := collectAll(ctx, ns, child, depth+1, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: routing keys
// ---------------------------------------------------------------------------

// routing is a parsed routing key: the hex form, as stored on leaf entries, plus
// its leading 32 bits. Parsing once per operation keeps the per-level index a
// shift and a mask instead of re-parsing the same 8 hex characters at every
// level of every descent.
type routing struct {
	hex    string
	prefix uint32
}

func newRouting(keyHex string) (routing, error) {
	if len(keyHex) < 8 {
		return routing{}, fmt.Errorf("key too short")
	}
	val, err := strconv.ParseUint(keyHex[:8], 16, 32)
	if err != nil {
		return routing{}, err
	}
	return routing{hex: keyHex, prefix: uint32(val)}, nil
}

// routingForEntry returns the routing key of a stored leaf entry, falling back
// to SHA256(Key) for legacy entries written before PathKey existed.
func routingForEntry(e leafEntry) (routing, error) {
	pk := e.PathKey
	if pk == "" {
		pk = core.ComputeHash([]byte(e.Key))
	}
	return newRouting(pk)
}

func (r routing) indexAt(level int) (int, error) {
	shift := 32 - (level+1)*bitsPerLevel
	if shift < 0 {
		return 0, fmt.Errorf("level too deep for 32-bit key prefix")
	}
	return int((r.prefix >> shift) & ((1 << bitsPerLevel) - 1)), nil
}

func sortEntries(entries []leafEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
}

// childPos returns the index of bucket idx within a node's packed children
// array, and whether the node has a child in that bucket at all.
func childPos(bitmap uint32, idx int) (int, bool) {
	bit := uint32(1) << idx
	return bits.OnesCount32(bitmap & (bit - 1)), bitmap&bit != 0
}
