package hamt

import (
	"context"
	"encoding/json"
	"fmt"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

const nodeCacheSize = 4096

// NodeStore is the only part of this package that knows HAMT nodes are bytes.
// It maps a node ref ("node/<sha256>") to a decoded *core.HAMTNode and back,
// owning the key prefix, the canonical encoding, and a read cache.
//
// Nodes are content-addressed, so a ref's decoded form never changes; the cache
// therefore needs no invalidation. For the same reason Load hands out a shared
// pointer: callers must treat the returned node as read-only and copy before
// mutating.
type NodeStore struct {
	store store.ObjectStore
	cache *lru.Cache[string, *core.HAMTNode]
}

// NewNodeStore returns a NodeStore reading and writing through s.
func NewNodeStore(s store.ObjectStore) *NodeStore {
	c, _ := lru.New[string, *core.HAMTNode](nodeCacheSize)
	return &NodeStore{store: s, cache: c}
}

// Load fetches and decodes the node identified by ref.
// The returned node is shared with the cache and must not be mutated.
func (ns *NodeStore) Load(ctx context.Context, ref string) (*core.HAMTNode, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty node ref")
	}
	if node, ok := ns.cache.Get(ref); ok {
		return node, nil
	}
	data, err := ns.store.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	var node core.HAMTNode
	if err := json.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	ns.cache.Add(ref, &node)
	return &node, nil
}

// Save encodes node canonically, writes it under its content address, and
// returns the resulting ref.
func (ns *NodeStore) Save(ctx context.Context, node *core.HAMTNode) (string, error) {
	hash, data, err := core.ComputeJSONHash(node)
	if err != nil {
		return "", err
	}
	ref := nodePrefix + hash
	if err := ns.store.Put(ctx, ref, data); err != nil {
		return "", err
	}
	ns.cache.Add(ref, node)
	return ref, nil
}
