package hamt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/errgroup"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

var defaultLog = logger.New("hamt", logger.ColorCyan)

const (
	nodeCacheSize       = 4096
	defaultWriteWorkers = 20

	// nodeCacheSizeV3 is the node cache's entry cap for a format-v3 tree. v3
	// leaves are sized by a byte budget (leafSplitBytesV3) rather than an entry
	// count, so 4096 of them could pin gigabytes; 128 caps the cache near
	// 128 × 512 KB = 64 MB worst case while still holding the working set of a
	// directory-clustered traversal, which revisits a handful of leaves at a
	// time (RFC 0026 §4).
	nodeCacheSizeV3 = 128
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
	cache *lru.Cache[string, *node]
	// log is this node store's debug sink. It always points at a logger; an
	// unbound one falls back to the process-wide writer.
	log *logger.Logger

	// v3 selects the format-v3 binary node encoding for every node this store
	// seals (RFC 0026). Reading is format-agnostic either way — load sniffs
	// the bytes — so the flag governs writes and the leaf split rule only.
	v3 bool
}

// NewNodeStore returns a NodeStore reading and writing through s.
func NewNodeStore(s store.ObjectStore) *NodeStore {
	c, _ := lru.New[string, *node](nodeCacheSize)
	return &NodeStore{store: s, cache: c, log: defaultLog}
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
		if size > leafSplitBytesV3 {
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
