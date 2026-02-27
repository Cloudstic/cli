package engine

import (
	"context"
	"encoding/json"

	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

const nodeCatalogKey = "index/nodes"

// NewCachedTree creates a HAMT tree backed by a TransactionalStore whose read
// cache is pre-populated from the persistent node catalog. This avoids
// individual B2 fetches for nodes that were seen in previous runs.
func NewCachedTree(s store.ObjectStore) *hamt.Tree {
	ts := hamt.NewTransactionalStore(s)
	ts.PreloadReadCache(LoadNodeCache(s))
	return hamt.NewTree(ts)
}

// NodeCatalog maps node refs (e.g. "node/<hash>") to their raw JSON bytes.
type NodeCatalog map[string]json.RawMessage

// LoadNodeCache reads the persisted node catalog from the store. Returns an
// empty map (not nil) when the catalog does not yet exist.
func LoadNodeCache(s store.ObjectStore) map[string][]byte {
	data, err := s.Get(context.Background(), nodeCatalogKey)
	if err != nil {
		return map[string][]byte{}
	}
	var cat NodeCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return map[string][]byte{}
	}
	out := make(map[string][]byte, len(cat))
	for k, v := range cat {
		out[k] = []byte(v)
	}
	debugf("node cache loaded: %d entries", len(out))
	return out
}

// SaveNodeCache persists all node data so the next run can skip B2 fetches.
func SaveNodeCache(s store.ObjectStore, nodes map[string][]byte) error {
	if len(nodes) == 0 {
		return nil
	}
	cat := make(NodeCatalog, len(nodes))
	for k, v := range nodes {
		cat[k] = json.RawMessage(v)
	}
	data, err := json.Marshal(cat)
	if err != nil {
		return err
	}
	debugf("node cache saved: %d entries", len(cat))
	return s.Put(context.Background(), nodeCatalogKey, data)
}
