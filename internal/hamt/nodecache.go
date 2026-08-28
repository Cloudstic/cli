package hamt

import (
	"container/list"
	"sync"
)

// nodeCache is an LRU over decoded nodes bounded by entry count and by
// approximate resident bytes, whichever binds first.
//
// A count bound alone is the wrong shape for format v3: leaves are sized by a
// byte budget, so a fixed entry count caps the cache at wildly different
// totals depending on leaf fill — 128 half-megabyte leaves is 64 MB, 128
// sawtooth leaves is 6 MB, and the first v2-versus-v3 benchmark showed the
// difference as a 39x request blowup, every per-entry lookup landing on a
// leaf the count bound had just evicted. Bounding bytes makes the cache mean
// what its budget says whatever the fill factor is. The count bound stays for
// the v2 shape, where nodes are small JSON documents and per-entry overhead,
// not payload bytes, is the resident cost.
//
// Sizes are approximate (see node.approxSize) and cached at insert: a node is
// immutable once decoded, so its size never changes.
type nodeCache struct {
	mu       sync.Mutex
	maxCount int
	maxBytes int
	bytes    int
	order    *list.List // front = most recent; values are *nodeCacheEntry
	items    map[string]*list.Element
}

type nodeCacheEntry struct {
	ref  string
	node *node
	size int
}

func newNodeCache(maxCount, maxBytes int) *nodeCache {
	return &nodeCache{
		maxCount: maxCount,
		maxBytes: maxBytes,
		order:    list.New(),
		items:    make(map[string]*list.Element),
	}
}

func (c *nodeCache) Get(ref string) (*node, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[ref]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*nodeCacheEntry).node, true
}

func (c *nodeCache) Add(ref string, n *node) {
	size := n.approxSize()
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[ref]; ok {
		c.order.MoveToFront(el)
		return
	}

	// A node larger than the whole byte budget is served but never cached:
	// admitting it would evict everything else to hold one entry.
	if size > c.maxBytes {
		return
	}

	el := c.order.PushFront(&nodeCacheEntry{ref: ref, node: n, size: size})
	c.items[ref] = el
	c.bytes += size

	for (c.order.Len() > c.maxCount || c.bytes > c.maxBytes) && c.order.Len() > 1 {
		oldest := c.order.Back()
		entry := oldest.Value.(*nodeCacheEntry)
		c.order.Remove(oldest)
		delete(c.items, entry.ref)
		c.bytes -= entry.size
	}
}

// Configure replaces the cache's bounds, evicting down to them.
func (c *nodeCache) Configure(maxCount, maxBytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxCount = maxCount
	c.maxBytes = maxBytes
	for (c.order.Len() > c.maxCount || c.bytes > c.maxBytes) && c.order.Len() > 0 {
		oldest := c.order.Back()
		entry := oldest.Value.(*nodeCacheEntry)
		c.order.Remove(oldest)
		delete(c.items, entry.ref)
		c.bytes -= entry.size
	}
}

// approxSize is the node's approximate resident footprint, used for the
// cache's byte accounting.
func (n *node) approxSize() int {
	size := 64
	if n.leaf {
		for i := range n.entries {
			size += n.entries[i].approxSize()
		}
		return size
	}
	for i := range n.children {
		size += len(n.children[i].ref) + 32
	}
	return size
}
