package hamt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

var defaultLog = logger.New("hamt", logger.ColorCyan)

const (
	nodeCacheSize       = 4096
	defaultWriteWorkers = 20

	// nodeCacheBytes is the byte budget for a v2 tree's node cache. v2 nodes
	// are small JSON documents, so the entry count binds first in practice and
	// this is a backstop against a pathological tree.
	nodeCacheBytes = 64 * 1024 * 1024

	// The v3 bounds. Leaves are sized in bytes, so the byte budget is the one
	// that means anything and the entry cap is set high enough not to bind
	// before it: capping at 128 entries (as this first did) held ~6 MB of a
	// ~60 MB leaf set and thrashed every per-entry lookup — measured as a 39x
	// request blowup against v2 on the 5,000-file tree.
	//
	// The byte budget is sized against the tree, not as a memory target,
	// because a miss is a whole leaf re-fetched over the network. A v3 backup
	// reads every leaf: change detection looks up each scanned entry in the
	// previous snapshot, and it does so in batches of entryBatch sorted by
	// routing key, so each batch sweeps the whole key space once. A cache that
	// holds the tree costs one sweep; a cache that does not costs one per
	// batch. That cliff is exactly what backup-dedup was falling off — a
	// 221 MB tree against a 128 MB cache re-read 438 distinct nodes 1,911
	// times, and raising the cache alone took the step from 2,067 requests to
	// 594 with no effect on what is written.
	//
	// 32 leaf budgets is the shape of the number rather than a round figure:
	// what the cache has to hold is a working set counted in leaves, so it has
	// to move when the leaf size does. A tree larger than this still falls off
	// the cliff — the cache can only move it, not remove it — which is why the
	// leaf budget carries the other half of the fix.
	nodeCacheSizeV3 = 8192

	// nodeCacheLeaves is that budget expressed in leaves, which is the unit it
	// means something in: the working set is counted in leaves, so the cache has
	// to move when the leaf size does. See nodeCacheBytes().
	nodeCacheLeaves = 32
)

// NodeStore is the only part of this package that knows HAMT nodes are bytes.
// It maps a node ref ("node/<sha256>") to a decoded node and back, owning the
// key prefix, the canonical encoding, and a read cache.
//
// Nodes are content-addressed, so a ref's decoded form never changes and the
// cache needs no invalidation. For the same reason Load hands out a shared
// pointer: a loaded node is clean, and clean nodes are immutable (see child).
type NodeStore struct {
	store store.ObjectStore
	cache *nodeCache
	// log is this node store's debug sink. It always points at a logger; an
	// unbound one falls back to the process-wide writer.
	log *logger.Logger

	// v3 selects the format-v3 binary node encoding for every node this store
	// seals (RFC 0026). Reading is format-agnostic either way — load sniffs
	// the bytes — so the flag governs writes, the leaf split rule, and the
	// routing shape below.
	v3 bool
}

// The routing shape of the tree this store belongs to. A tree's arity decides
// how an overflowing leaf is partitioned, and therefore how full its leaves
// end up; v3 uses a narrower split for that reason (see bitsPerLevelV3).
//
// They are methods rather than fields so a NodeStore built before the option
// was applied cannot be left describing the wrong shape.
func (ns *NodeStore) bits() int {
	if ns.v3 {
		return bitsPerLevelV3
	}
	return bitsPerLevel
}

func (ns *NodeStore) branching() int {
	if ns.v3 {
		return branchingV3
	}
	return branching
}

func (ns *NodeStore) maxDepth() int {
	if ns.v3 {
		return maxDepthV3
	}
	return maxDepth
}

// maxTreeDepth bounds every recursive descent: routing bits run out after
// maxDepth internal levels, so anything deeper means the structure is not a
// tree. Two levels of slack keep the guard from firing on a legitimate leaf
// below the deepest internal node.
func (ns *NodeStore) maxTreeDepth() int {
	return ns.maxDepth() + 2
}

// NewNodeStore returns a NodeStore reading and writing through s.
func NewNodeStore(s store.ObjectStore) *NodeStore {
	return &NodeStore{store: s, cache: newNodeCache(nodeCacheSize, nodeCacheBytes), log: defaultLog}
}

// load fetches and decodes the node identified by ref. The returned node is
// clean: it is shared with the cache and must not be mutated.
func (ns *NodeStore) load(ctx context.Context, ref string) (*node, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty node ref")
	}
	if n, ok := ns.cache.Get(ref); ok {
		return n, nil
	}
	data, err := ns.store.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := verifyNodeRef(ref, data); err != nil {
		return nil, err
	}

	// The two encodings are distinguished by their first bytes: a v2 node is a
	// JSON object and starts with '{', a v3 node with its magic. Sniffing here
	// rather than consulting ns.v3 keeps reading format-agnostic — the flag
	// decides what this store writes, never what it can read.
	var n *node
	if isV3NodeData(data) {
		if n, err = decodeNodeV3(data); err != nil {
			return nil, fmt.Errorf("decode v3 node %s: %w", ref, err)
		}
	} else {
		var sn storedNode
		if err := json.Unmarshal(data, &sn); err != nil {
			return nil, fmt.Errorf("decode node %s: %w", ref, err)
		}
		n = decodeNode(&sn)
	}
	ns.cache.Add(ref, n)
	return n, nil
}

// seal encodes sn under its content address and returns the ref plus the bytes
// to write. It performs no I/O, so callers can compute a root ref without
// committing to it.
func (ns *NodeStore) seal(sn *storedNode) (string, []byte, error) {
	if ns.v3 {
		data := encodeNodeV3(sn)
		return nodePrefix + core.ComputeHash(data), data, nil
	}
	hash, data, err := core.ComputeJSONHash(sn)
	if err != nil {
		return "", nil, err
	}
	return nodePrefix + hash, data, nil
}

// putAll writes a batch of sealed nodes concurrently. Node writes are
// independent — every ref is a content address, so order and duplication are
// both harmless — which is what makes the fan-out safe.
func (ns *NodeStore) putAll(ctx context.Context, batch map[string][]byte) error {
	if len(batch) == 0 {
		return nil
	}

	var totalBytes int
	for _, data := range batch {
		totalBytes += len(data)
	}
	ns.log.Debugf("commit: writing %d nodes (%d bytes total)", len(batch), totalBytes)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(min(store.GetConcurrencyHint(ns.store, defaultWriteWorkers), len(batch)))

	for key, data := range batch {
		g.Go(func() error {
			if err := ns.store.Put(ctx, key, data); err != nil {
				return fmt.Errorf("write node %s: %w", key, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// leafOverfull reports whether a leaf holding entries is over its split
// budget. It is the single split predicate: insertion, buildNode's partition
// base case, and delete's collapse all consult it, which is what keeps a
// tree's shape a pure function of its contents in both formats.
//
// v2 splits on entry count. v3 splits on encoded bytes, with an entry cap so
// a leaf of tiny entries still bounds its in-leaf linear scans (RFC 0026 §2).
func (ns *NodeStore) leafOverfull(entries []leafEntry) bool {
	if !ns.v3 {
		return len(entries) > maxLeafSize
	}
	if len(entries) > maxLeafEntriesV3 {
		return true
	}
	var size int
	for i := range entries {
		size += entries[i].approxSize()
		if size > v3LeafSplitBytes() {
			return true
		}
	}
	return false
}

// verifyNodeRef checks that data is what ref names.
//
// A node ref is its own checksum: "node/" + SHA-256 of the bytes written under
// it. Checking it on the way in means every consumer of the tree — restore,
// ls, diff, prune, check — detects a corrupted or substituted node, not just
// `check -read-data`. It also closes the recursion hole: a node cannot claim a
// child that hashes to one of its own ancestors without failing this check.
//
// The check itself lives in core alongside the same check for filemeta and
// snapshot objects, so the three links of the Merkle chain cannot drift apart.
func verifyNodeRef(ref string, data []byte) error {
	if !strings.HasPrefix(ref, nodePrefix) {
		return fmt.Errorf("node ref %q is missing the %q prefix", ref, nodePrefix)
	}
	return core.VerifyRef(ref, data)
}
