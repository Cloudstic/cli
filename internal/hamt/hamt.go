package hamt

import (
	"context"
	"encoding/json"
	"fmt"
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
)

// DiffEntry represents a single change between two trees.
// OldValue is empty for additions; NewValue is empty for deletions.
type DiffEntry struct {
	Key      string
	OldValue string
	NewValue string
}

// Tree is a persistent Hash Array Mapped Trie backed by a content-addressed store.
// Keys and values are opaque strings; values are typically object refs ("filemeta/<hash>").
type Tree struct {
	store store.ObjectStore
}

// NewTree creates a Tree backed by the given object store.
func NewTree(s store.ObjectStore) *Tree {
	return &Tree{store: s}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Insert adds or updates the entry for key, returning a new root ref.
// Pass an empty root to start a new tree.
func (t *Tree) Insert(root, key, value string) (string, error) {
	pathKey := computePathKey(key)
	return t.insertAt(root, pathKey, key, value, 0)
}

// Lookup returns the value associated with key, or ("", nil) if not found.
func (t *Tree) Lookup(root, key string) (string, error) {
	if root == "" {
		return "", nil
	}
	pathKey := computePathKey(key)
	return t.lookupAt(root, pathKey, key, 0)
}

// Walk visits every (key, value) pair stored in the tree rooted at root.
func (t *Tree) Walk(root string, fn func(key, value string) error) error {
	if root == "" {
		return nil
	}
	return t.walk(root, fn)
}

// Diff structurally compares two trees and calls fn for every entry that was
// added, removed, or modified between root1 and root2.
func (t *Tree) Diff(root1, root2 string, fn func(DiffEntry) error) error {
	if root1 == root2 {
		return nil
	}
	n1, err := t.loadNode(root1)
	if err != nil {
		return err
	}
	n2, err := t.loadNode(root2)
	if err != nil {
		return err
	}
	return t.diffNodes(n1, n2, 0, fn)
}

// NodeRefs visits every HAMT node ref reachable from root (including root itself).
// This is useful for garbage-collection marking.
func (t *Tree) NodeRefs(root string, fn func(ref string) error) error {
	if root == "" {
		return nil
	}
	return t.nodeRefs(root, fn)
}

// Delete removes the entry for key, returning a new root ref. If the key is
// not found the original root is returned unchanged. Deleting from an empty
// tree is a no-op.
func (t *Tree) Delete(root, key string) (string, error) {
	if root == "" {
		return "", nil
	}
	pathKey := computePathKey(key)
	newRef, err := t.deleteAt(root, pathKey, key, 0)
	if err != nil {
		return "", err
	}
	return newRef, nil
}

// ---------------------------------------------------------------------------
// Internal: persistence
// ---------------------------------------------------------------------------

func (t *Tree) loadNode(ref string) (*core.HAMTNode, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty node ref")
	}
	data, err := t.store.Get(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	var node core.HAMTNode
	if err := json.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	return &node, nil
}

func (t *Tree) saveNode(node *core.HAMTNode) (string, error) {
	hash, data, err := core.ComputeJSONHash(node)
	if err != nil {
		return "", err
	}
	key := "node/" + hash
	if err := t.store.Put(context.Background(), key, data); err != nil {
		return "", err
	}
	return key, nil
}

// ---------------------------------------------------------------------------
// Internal: path key helpers
// ---------------------------------------------------------------------------

func computePathKey(id string) string {
	return core.ComputeHash([]byte(id))
}

func indexForLevel(keyHex string, level int) (int, error) {
	if len(keyHex) < 8 {
		return 0, fmt.Errorf("key too short")
	}
	val, err := strconv.ParseUint(keyHex[:8], 16, 32)
	if err != nil {
		return 0, err
	}
	shift := 32 - (level+1)*bitsPerLevel
	if shift < 0 {
		return 0, fmt.Errorf("level too deep for 32-bit key prefix")
	}
	mask := uint64((1 << bitsPerLevel) - 1)
	return int((val >> shift) & mask), nil
}

func popcount(n uint32) int {
	count := 0
	for n > 0 {
		n &= n - 1
		count++
	}
	return count
}

func sortEntries(entries []core.LeafEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
}

// ---------------------------------------------------------------------------
// Internal: insert
// ---------------------------------------------------------------------------

func (t *Tree) insertAt(nodeRef, pathKey, key, value string, level int) (string, error) {
	var node *core.HAMTNode
	var err error

	if nodeRef == "" {
		node = &core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: []core.LeafEntry{}}
	} else {
		node, err = t.loadNode(nodeRef)
		if err != nil {
			return "", fmt.Errorf("load node %s: %w", nodeRef, err)
		}
	}

	if node.Type == core.ObjectTypeLeaf {
		return t.insertIntoLeaf(node, pathKey, key, value, level)
	}

	return t.insertIntoInternal(node, pathKey, key, value, level)
}

func (t *Tree) insertIntoLeaf(node *core.HAMTNode, pathKey, key, value string, level int) (string, error) {
	// Update existing entry.
	for i, e := range node.Entries {
		if e.Key == key {
			newEntries := make([]core.LeafEntry, len(node.Entries))
			copy(newEntries, node.Entries)
			newEntries[i] = core.LeafEntry{Key: key, FileMeta: value}
			return t.saveNode(&core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: newEntries})
		}
	}

	// Append if leaf has room or we're at max depth.
	if len(node.Entries) < maxLeafSize || level >= maxDepth {
		newEntries := make([]core.LeafEntry, len(node.Entries)+1)
		copy(newEntries, node.Entries)
		newEntries[len(node.Entries)] = core.LeafEntry{Key: key, FileMeta: value}
		sortEntries(newEntries)
		return t.saveNode(&core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: newEntries})
	}

	// Leaf full: split into an internal node.
	all := make([]core.LeafEntry, len(node.Entries)+1)
	copy(all, node.Entries)
	all[len(node.Entries)] = core.LeafEntry{Key: key, FileMeta: value}
	return t.buildNode(all, level)
}

func (t *Tree) insertIntoInternal(node *core.HAMTNode, pathKey, key, value string, level int) (string, error) {
	idx, err := indexForLevel(pathKey, level)
	if err != nil {
		return "", err
	}

	bit := uint32(1 << idx)
	exists := node.Bitmap&bit != 0
	childPos := popcount(node.Bitmap & (bit - 1))

	var childRef string
	if exists {
		childRef = node.Children[childPos]
	}

	newChildRef, err := t.insertAt(childRef, pathKey, key, value, level+1)
	if err != nil {
		return "", err
	}

	newNode := core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: node.Bitmap}

	if !exists {
		newNode.Bitmap |= bit
		childPos = popcount(newNode.Bitmap & (bit - 1))
		newChildren := make([]string, len(node.Children)+1)
		copy(newChildren[:childPos], node.Children[:childPos])
		newChildren[childPos] = newChildRef
		copy(newChildren[childPos+1:], node.Children[childPos:])
		newNode.Children = newChildren
	} else {
		newChildren := make([]string, len(node.Children))
		copy(newChildren, node.Children)
		newChildren[childPos] = newChildRef
		newNode.Children = newChildren
	}

	return t.saveNode(&newNode)
}

// buildNode recursively partitions entries into a tree starting at level.
func (t *Tree) buildNode(entries []core.LeafEntry, level int) (string, error) {
	if len(entries) <= maxLeafSize || level >= maxDepth {
		sortEntries(entries)
		return t.saveNode(&core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: entries})
	}

	buckets := make(map[int][]core.LeafEntry)
	for _, e := range entries {
		pk := computePathKey(e.Key)
		idx, err := indexForLevel(pk, level)
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
		ref, err := t.buildNode(bucket, level+1)
		if err != nil {
			return "", err
		}
		bitmap |= 1 << i
		children = append(children, ref)
	}

	return t.saveNode(&core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: bitmap, Children: children})
}

// ---------------------------------------------------------------------------
// Internal: delete
// ---------------------------------------------------------------------------

func (t *Tree) deleteAt(nodeRef, pathKey, key string, level int) (string, error) {
	node, err := t.loadNode(nodeRef)
	if err != nil {
		return nodeRef, err
	}

	if node.Type == core.ObjectTypeLeaf {
		return t.deleteFromLeaf(node, key)
	}

	return t.deleteFromInternal(node, nodeRef, pathKey, key, level)
}

func (t *Tree) deleteFromLeaf(node *core.HAMTNode, key string) (string, error) {
	idx := -1
	for i, e := range node.Entries {
		if e.Key == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Key not present; return the node unchanged. We need to re-save to
		// get its ref, but since content hasn't changed saveNode is
		// deterministic and will return the same ref.
		return t.saveNode(node)
	}

	newEntries := make([]core.LeafEntry, 0, len(node.Entries)-1)
	newEntries = append(newEntries, node.Entries[:idx]...)
	newEntries = append(newEntries, node.Entries[idx+1:]...)

	if len(newEntries) == 0 {
		return "", nil
	}
	return t.saveNode(&core.HAMTNode{Type: core.ObjectTypeLeaf, Entries: newEntries})
}

func (t *Tree) deleteFromInternal(node *core.HAMTNode, nodeRef, pathKey, key string, level int) (string, error) {
	idx, err := indexForLevel(pathKey, level)
	if err != nil {
		return nodeRef, err
	}

	bit := uint32(1 << idx)
	if node.Bitmap&bit == 0 {
		return nodeRef, nil
	}

	childPos := popcount(node.Bitmap & (bit - 1))
	newChildRef, err := t.deleteAt(node.Children[childPos], pathKey, key, level+1)
	if err != nil {
		return nodeRef, err
	}

	if newChildRef == node.Children[childPos] {
		return nodeRef, nil
	}

	if newChildRef == "" {
		// Child became empty; remove the slot from this internal node.
		newBitmap := node.Bitmap &^ bit
		if newBitmap == 0 {
			return "", nil
		}
		newChildren := make([]string, 0, len(node.Children)-1)
		newChildren = append(newChildren, node.Children[:childPos]...)
		newChildren = append(newChildren, node.Children[childPos+1:]...)

		// Collapse: if only one child remains and it is a leaf, promote it.
		if len(newChildren) == 1 {
			child, err := t.loadNode(newChildren[0])
			if err == nil && child.Type == core.ObjectTypeLeaf {
				return t.saveNode(child)
			}
		}
		return t.saveNode(&core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: newBitmap, Children: newChildren})
	}

	// Child still exists but changed; update in place.
	newChildren := make([]string, len(node.Children))
	copy(newChildren, node.Children)
	newChildren[childPos] = newChildRef
	return t.saveNode(&core.HAMTNode{Type: core.ObjectTypeInternal, Bitmap: node.Bitmap, Children: newChildren})
}

// ---------------------------------------------------------------------------
// Internal: lookup
// ---------------------------------------------------------------------------

func (t *Tree) lookupAt(nodeRef, pathKey, key string, level int) (string, error) {
	node, err := t.loadNode(nodeRef)
	if err != nil {
		return "", err
	}

	if node.Type == core.ObjectTypeLeaf {
		for _, e := range node.Entries {
			if e.Key == key {
				return e.FileMeta, nil
			}
		}
		return "", nil
	}

	idx, err := indexForLevel(pathKey, level)
	if err != nil {
		return "", err
	}

	bit := uint32(1 << idx)
	if node.Bitmap&bit == 0 {
		return "", nil
	}

	childPos := popcount(node.Bitmap & (bit - 1))
	if childPos >= len(node.Children) {
		return "", fmt.Errorf("corrupt node: bitmap indicates child but array too short")
	}
	return t.lookupAt(node.Children[childPos], pathKey, key, level+1)
}

// ---------------------------------------------------------------------------
// Internal: walk
// ---------------------------------------------------------------------------

func (t *Tree) walk(nodeRef string, fn func(key, value string) error) error {
	node, err := t.loadNode(nodeRef)
	if err != nil {
		return err
	}

	if node.Type == core.ObjectTypeLeaf {
		for _, e := range node.Entries {
			if err := fn(e.Key, e.FileMeta); err != nil {
				return err
			}
		}
		return nil
	}

	for _, childRef := range node.Children {
		if err := t.walk(childRef, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: diff
// ---------------------------------------------------------------------------

func (t *Tree) diffNodes(n1, n2 *core.HAMTNode, level int, fn func(DiffEntry) error) error {
	if n1.Type == core.ObjectTypeLeaf && n2.Type == core.ObjectTypeLeaf {
		return t.diffLeaves(n1, n2, fn)
	}

	for i := 0; i < branching; i++ {
		c1, err := t.childForBucket(n1, i, level)
		if err != nil {
			return err
		}
		c2, err := t.childForBucket(n2, i, level)
		if err != nil {
			return err
		}

		if c1 == nil && c2 == nil {
			continue
		}
		if c1 == nil {
			if err := t.collectAll(c2, func(k, v string) error { return fn(DiffEntry{Key: k, NewValue: v}) }); err != nil {
				return err
			}
			continue
		}
		if c2 == nil {
			if err := t.collectAll(c1, func(k, v string) error { return fn(DiffEntry{Key: k, OldValue: v}) }); err != nil {
				return err
			}
			continue
		}
		if err := t.diffNodes(c1, c2, level+1, fn); err != nil {
			return err
		}
	}
	return nil
}

// childForBucket returns the child node for bucket idx.
// When the node is a leaf acting as a virtual internal (mixed-type comparison),
// it returns a synthetic leaf containing only entries that hash to this bucket.
func (t *Tree) childForBucket(n *core.HAMTNode, idx, level int) (*core.HAMTNode, error) {
	if n.Type == core.ObjectTypeInternal {
		bit := uint32(1 << idx)
		if n.Bitmap&bit == 0 {
			return nil, nil
		}
		pos := popcount(n.Bitmap & (bit - 1))
		return t.loadNode(n.Children[pos])
	}

	// Leaf: filter entries belonging to this bucket.
	var filtered []core.LeafEntry
	for _, e := range n.Entries {
		pk := computePathKey(e.Key)
		i, err := indexForLevel(pk, level)
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

func (t *Tree) diffLeaves(n1, n2 *core.HAMTNode, fn func(DiffEntry) error) error {
	left := make(map[string]string, len(n1.Entries))
	for _, e := range n1.Entries {
		left[e.Key] = e.FileMeta
	}

	for _, e := range n2.Entries {
		old, ok := left[e.Key]
		if !ok {
			if err := fn(DiffEntry{Key: e.Key, NewValue: e.FileMeta}); err != nil {
				return err
			}
		} else {
			if old != e.FileMeta {
				if err := fn(DiffEntry{Key: e.Key, OldValue: old, NewValue: e.FileMeta}); err != nil {
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

func (t *Tree) collectAll(node *core.HAMTNode, fn func(key, value string) error) error {
	if node.Type == core.ObjectTypeLeaf {
		for _, e := range node.Entries {
			if err := fn(e.Key, e.FileMeta); err != nil {
				return err
			}
		}
		return nil
	}
	for _, ref := range node.Children {
		child, err := t.loadNode(ref)
		if err != nil {
			return err
		}
		if err := t.collectAll(child, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: node refs (GC)
// ---------------------------------------------------------------------------

func (t *Tree) nodeRefs(ref string, fn func(string) error) error {
	if err := fn(ref); err != nil {
		return err
	}
	node, err := t.loadNode(ref)
	if err != nil {
		return err
	}
	if node.Type == core.ObjectTypeInternal {
		for _, childRef := range node.Children {
			if err := t.nodeRefs(childRef, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
