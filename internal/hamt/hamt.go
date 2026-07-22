package hamt

import (
	"context"
	"fmt"
	"math/bits"
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
)

// DiffEntry represents a single change between two trees.
// OldValue is empty for additions; NewValue is empty for deletions.
type DiffEntry struct {
	Key      string
	OldValue string
	NewValue string
}

// Tree is a persistent Hash Array Mapped Trie.
//
// It is a map from (routing key, key) to an opaque string value. The routing
// key is a hex string chosen by the caller; its first 32 bits decide the path
// through the trie, so callers that want locality between related keys give
// them a shared prefix. The key identifies the entry within its leaf and the
// value is opaque — callers store object refs there, but the tree never
// interprets them.
//
// All persistence is delegated to NodeStore; nothing below this line knows
// that nodes are bytes.
type Tree struct {
	nodes *NodeStore
}

// NewTree creates a Tree backed by the given object store.
func NewTree(s store.ObjectStore) *Tree {
	return &Tree{nodes: NewNodeStore(s)}
}

// NewTreeWithNodes creates a Tree over an existing NodeStore, so that several
// trees can share one read cache.
func NewTreeWithNodes(ns *NodeStore) *Tree {
	return &Tree{nodes: ns}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// errFoundSentinel is used to short-circuit a Walk in LookupByKey.
var errFoundSentinel = fmt.Errorf("found")

// Insert adds or updates the entry for key, returning a new root ref.
// routingKey decides the entry's path through the trie; key is stored as the
// leaf key. Pass an empty root to start a new tree.
func (t *Tree) Insert(ctx context.Context, root, routingKey, key, value string) (string, error) {
	r, err := newRouting(routingKey)
	if err != nil {
		return "", err
	}
	return t.insertAt(ctx, root, r, key, value, 0)
}

// Lookup returns the value associated with key, or ("", nil) if not found.
// routingKey must be the same routing key the entry was inserted under.
func (t *Tree) Lookup(ctx context.Context, root, routingKey, key string) (string, error) {
	if root == "" {
		return "", nil
	}
	r, err := newRouting(routingKey)
	if err != nil {
		return "", err
	}
	return t.lookupAt(ctx, root, r, key, 0)
}

// LookupByKey finds a value by walking the entire tree and matching on the raw
// key. This is O(N) and slower than Lookup, but does not require the entry's
// routing key. Use only when the routing key cannot be reconstructed.
func (t *Tree) LookupByKey(ctx context.Context, root, key string) (string, error) {
	if root == "" {
		return "", nil
	}
	var found string
	err := t.Walk(ctx, root, func(k, value string) error {
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

// Walk visits every (key, value) pair stored in the tree rooted at root.
func (t *Tree) Walk(ctx context.Context, root string, fn func(key, value string) error) error {
	if root == "" {
		return nil
	}
	return t.walk(ctx, root, fn)
}

// Diff structurally compares two trees and calls fn for every entry that was
// added, removed, or modified between root1 and root2.
func (t *Tree) Diff(ctx context.Context, root1, root2 string, fn func(DiffEntry) error) error {
	if root1 == root2 {
		return nil
	}
	n1, err := t.nodes.Load(ctx, root1)
	if err != nil {
		return err
	}
	n2, err := t.nodes.Load(ctx, root2)
	if err != nil {
		return err
	}
	return t.diffNodes(ctx, n1, n2, 0, fn)
}

// NodeRefs visits every HAMT node ref reachable from root (including root itself).
// This is useful for garbage-collection marking.
func (t *Tree) NodeRefs(ctx context.Context, root string, fn func(ref string) error) error {
	if root == "" {
		return nil
	}
	return t.nodeRefs(ctx, root, fn)
}

// Delete removes the entry for key, returning a new root ref.
// If the key is not found the original root is returned unchanged.
// Deleting from an empty tree is a no-op.
func (t *Tree) Delete(ctx context.Context, root, routingKey, key string) (string, error) {
	if root == "" {
		return "", nil
	}
	r, err := newRouting(routingKey)
	if err != nil {
		return "", err
	}
	newRef, err := t.deleteAt(ctx, root, r, key, 0)
	if err != nil {
		return "", err
	}
	return newRef, nil
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
func routingForEntry(e core.LeafEntry) (routing, error) {
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
	mask := uint32((1 << bitsPerLevel) - 1)
	return int((r.prefix >> shift) & mask), nil
}

func sortEntries(entries []core.LeafEntry) {
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

// ---------------------------------------------------------------------------
// Internal: insert
// ---------------------------------------------------------------------------

func (t *Tree) insertAt(ctx context.Context, nodeRef string, r routing, key, value string, level int) (string, error) {
	var node *core.HAMTNode
	var err error

	if nodeRef == "" {
		node = &core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: []core.LeafEntry{}}
	} else {
		node, err = t.nodes.Load(ctx, nodeRef)
		if err != nil {
			return "", fmt.Errorf("load node %s: %w", nodeRef, err)
		}
	}

	if node.Type == core.ObjectTypeLeaf {
		return t.insertIntoLeaf(ctx, node, r, key, value, level)
	}

	return t.insertIntoInternal(ctx, node, r, key, value, level)
}

func (t *Tree) insertIntoLeaf(ctx context.Context, node *core.HAMTNode, r routing, key, value string, level int) (string, error) {
	entry := core.LeafEntry{Key: key, PathKey: r.hex, Value: value}

	// Update existing entry.
	for i, e := range node.Entries {
		if e.Key == key {
			newEntries := make([]core.LeafEntry, len(node.Entries))
			copy(newEntries, node.Entries)
			newEntries[i] = entry
			return t.nodes.Save(ctx, &core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: newEntries})
		}
	}

	// Append if leaf has room or we're at max depth.
	if len(node.Entries) < maxLeafSize || level >= maxDepth {
		newEntries := make([]core.LeafEntry, len(node.Entries)+1)
		copy(newEntries, node.Entries)
		newEntries[len(node.Entries)] = entry
		sortEntries(newEntries)
		return t.nodes.Save(ctx, &core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: newEntries})
	}

	// Leaf full: split into an internal node.
	all := make([]core.LeafEntry, len(node.Entries)+1)
	copy(all, node.Entries)
	all[len(node.Entries)] = entry
	return t.buildNode(ctx, all, level)
}

func (t *Tree) insertIntoInternal(ctx context.Context, node *core.HAMTNode, r routing, key, value string, level int) (string, error) {
	idx, err := r.indexAt(level)
	if err != nil {
		return "", err
	}

	pos, exists := childPos(node.Bitmap, idx)

	var childRef string
	if exists {
		childRef = node.Children[pos]
	}

	newChildRef, err := t.insertAt(ctx, childRef, r, key, value, level+1)
	if err != nil {
		return "", err
	}

	newNode := core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: node.Bitmap}

	if !exists {
		newNode.Bitmap |= uint32(1) << idx
		newChildren := make([]string, len(node.Children)+1)
		copy(newChildren[:pos], node.Children[:pos])
		newChildren[pos] = newChildRef
		copy(newChildren[pos+1:], node.Children[pos:])
		newNode.Children = newChildren
	} else {
		newChildren := make([]string, len(node.Children))
		copy(newChildren, node.Children)
		newChildren[pos] = newChildRef
		newNode.Children = newChildren
	}

	return t.nodes.Save(ctx, &newNode)
}

// buildNode recursively partitions entries into a tree starting at level.
func (t *Tree) buildNode(ctx context.Context, entries []core.LeafEntry, level int) (string, error) {
	if len(entries) <= maxLeafSize || level >= maxDepth {
		sortEntries(entries)
		return t.nodes.Save(ctx, &core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: entries})
	}

	buckets := make(map[int][]core.LeafEntry)
	for _, e := range entries {
		r, err := routingForEntry(e)
		if err != nil {
			return "", err
		}
		idx, err := r.indexAt(level)
		if err != nil {
			return "", err
		}
		buckets[idx] = append(buckets[idx], e)
	}

	var children []string
	var bitmap uint32
	for i := 0; i < branching; i++ {
		bucket, ok := buckets[i]
		if !ok {
			continue
		}
		ref, err := t.buildNode(ctx, bucket, level+1)
		if err != nil {
			return "", err
		}
		bitmap |= 1 << i
		children = append(children, ref)
	}

	return t.nodes.Save(ctx, &core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: bitmap, Children: children})
}

// ---------------------------------------------------------------------------
// Internal: delete
// ---------------------------------------------------------------------------

func (t *Tree) deleteAt(ctx context.Context, nodeRef string, r routing, key string, level int) (string, error) {
	node, err := t.nodes.Load(ctx, nodeRef)
	if err != nil {
		return nodeRef, err
	}

	if node.Type == core.ObjectTypeLeaf {
		return t.deleteFromLeaf(ctx, node, nodeRef, key)
	}

	return t.deleteFromInternal(ctx, node, nodeRef, r, key, level)
}

func (t *Tree) deleteFromLeaf(ctx context.Context, node *core.HAMTNode, nodeRef, key string) (string, error) {
	idx := -1
	for i, e := range node.Entries {
		if e.Key == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Key not present; the node is unchanged, so its ref is unchanged too.
		return nodeRef, nil
	}

	newEntries := make([]core.LeafEntry, 0, len(node.Entries)-1)
	newEntries = append(newEntries, node.Entries[:idx]...)
	newEntries = append(newEntries, node.Entries[idx+1:]...)

	if len(newEntries) == 0 {
		return "", nil
	}
	return t.nodes.Save(ctx, &core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: newEntries})
}

func (t *Tree) deleteFromInternal(ctx context.Context, node *core.HAMTNode, nodeRef string, r routing, key string, level int) (string, error) {
	idx, err := r.indexAt(level)
	if err != nil {
		return nodeRef, err
	}

	pos, exists := childPos(node.Bitmap, idx)
	if !exists {
		return nodeRef, nil
	}

	newChildRef, err := t.deleteAt(ctx, node.Children[pos], r, key, level+1)
	if err != nil {
		return nodeRef, err
	}

	if newChildRef == node.Children[pos] {
		return nodeRef, nil
	}

	if newChildRef == "" {
		// Child became empty; remove the slot from this internal node.
		newBitmap := node.Bitmap &^ (uint32(1) << idx)
		if newBitmap == 0 {
			return "", nil
		}
		newChildren := make([]string, 0, len(node.Children)-1)
		newChildren = append(newChildren, node.Children[:pos]...)
		newChildren = append(newChildren, node.Children[pos+1:]...)

		// Collapse: if only one child remains and it is a leaf, promote it.
		if len(newChildren) == 1 {
			child, err := t.nodes.Load(ctx, newChildren[0])
			if err == nil && child.Type == core.ObjectTypeLeaf {
				return newChildren[0], nil
			}
		}
		return t.nodes.Save(ctx, &core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: newBitmap, Children: newChildren})
	}

	// Child still exists but changed; update in place.
	newChildren := make([]string, len(node.Children))
	copy(newChildren, node.Children)
	newChildren[pos] = newChildRef
	return t.nodes.Save(ctx, &core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: node.Bitmap, Children: newChildren})
}

// ---------------------------------------------------------------------------
// Internal: lookup
// ---------------------------------------------------------------------------

func (t *Tree) lookupAt(ctx context.Context, nodeRef string, r routing, key string, level int) (string, error) {
	node, err := t.nodes.Load(ctx, nodeRef)
	if err != nil {
		return "", err
	}

	if node.Type == core.ObjectTypeLeaf {
		for _, e := range node.Entries {
			if e.Key == key {
				return e.Value, nil
			}
		}
		return "", nil
	}

	idx, err := r.indexAt(level)
	if err != nil {
		return "", err
	}

	pos, exists := childPos(node.Bitmap, idx)
	if !exists {
		return "", nil
	}
	if pos >= len(node.Children) {
		return "", fmt.Errorf("corrupt node: bitmap indicates child but array too short")
	}
	return t.lookupAt(ctx, node.Children[pos], r, key, level+1)
}

// ---------------------------------------------------------------------------
// Internal: walk
// ---------------------------------------------------------------------------

func (t *Tree) walk(ctx context.Context, nodeRef string, fn func(key, value string) error) error {
	node, err := t.nodes.Load(ctx, nodeRef)
	if err != nil {
		return err
	}

	if node.Type == core.ObjectTypeLeaf {
		for _, e := range node.Entries {
			if err := fn(e.Key, e.Value); err != nil {
				return err
			}
		}
		return nil
	}

	for _, childRef := range node.Children {
		if err := t.walk(ctx, childRef, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: diff
// ---------------------------------------------------------------------------

func (t *Tree) diffNodes(ctx context.Context, n1, n2 *core.HAMTNode, level int, fn func(DiffEntry) error) error {
	if n1.Type == core.ObjectTypeLeaf && n2.Type == core.ObjectTypeLeaf {
		return diffLeaves(n1, n2, fn)
	}

	for i := 0; i < branching; i++ {
		c1, err := t.childForBucket(ctx, n1, i, level)
		if err != nil {
			return err
		}
		c2, err := t.childForBucket(ctx, n2, i, level)
		if err != nil {
			return err
		}

		if c1 == nil && c2 == nil {
			continue
		}
		if c1 == nil {
			if err := t.collectAll(ctx, c2, func(k, v string) error { return fn(DiffEntry{Key: k, NewValue: v}) }); err != nil {
				return err
			}
			continue
		}
		if c2 == nil {
			if err := t.collectAll(ctx, c1, func(k, v string) error { return fn(DiffEntry{Key: k, OldValue: v}) }); err != nil {
				return err
			}
			continue
		}
		if err := t.diffNodes(ctx, c1, c2, level+1, fn); err != nil {
			return err
		}
	}
	return nil
}

// childForBucket returns the child node for bucket idx.
// When the node is a leaf acting as a virtual internal (mixed-type comparison),
// it returns a synthetic leaf containing only entries that hash to this bucket.
func (t *Tree) childForBucket(ctx context.Context, n *core.HAMTNode, idx, level int) (*core.HAMTNode, error) {
	if n.Type == core.ObjectTypeInternal {
		pos, exists := childPos(n.Bitmap, idx)
		if !exists {
			return nil, nil
		}
		return t.nodes.Load(ctx, n.Children[pos])
	}

	// Leaf: filter entries belonging to this bucket.
	var filtered []core.LeafEntry
	for _, e := range n.Entries {
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
	return &core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: filtered}, nil
}

func diffLeaves(n1, n2 *core.HAMTNode, fn func(DiffEntry) error) error {
	left := make(map[string]string, len(n1.Entries))
	for _, e := range n1.Entries {
		left[e.Key] = e.Value
	}

	for _, e := range n2.Entries {
		old, ok := left[e.Key]
		if !ok {
			if err := fn(DiffEntry{Key: e.Key, NewValue: e.Value}); err != nil {
				return err
			}
		} else {
			if old != e.Value {
				if err := fn(DiffEntry{Key: e.Key, OldValue: old, NewValue: e.Value}); err != nil {
					return err
				}
			}
			delete(left, e.Key)
		}
	}

	for k, v := range left {
		if err := fn(DiffEntry{Key: k, OldValue: v}); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tree) collectAll(ctx context.Context, node *core.HAMTNode, fn func(key, value string) error) error {
	if node.Type == core.ObjectTypeLeaf {
		for _, e := range node.Entries {
			if err := fn(e.Key, e.Value); err != nil {
				return err
			}
		}
		return nil
	}
	for _, ref := range node.Children {
		child, err := t.nodes.Load(ctx, ref)
		if err != nil {
			return err
		}
		if err := t.collectAll(ctx, child, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: node refs (GC)
// ---------------------------------------------------------------------------

func (t *Tree) nodeRefs(ctx context.Context, ref string, fn func(string) error) error {
	if err := fn(ref); err != nil {
		return err
	}
	node, err := t.nodes.Load(ctx, ref)
	if err != nil {
		return err
	}
	if node.Type == core.ObjectTypeInternal {
		for _, childRef := range node.Children {
			if err := t.nodeRefs(ctx, childRef, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
