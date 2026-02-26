package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

// LsSnapshotOption configures an ls-snapshot operation.
type LsSnapshotOption func(*lsSnapshotConfig)

type lsSnapshotConfig struct{}

// LsSnapshotResult holds the data returned by an ls-snapshot operation.
type LsSnapshotResult struct {
	Ref        string
	Snapshot   core.Snapshot
	RootRefs   []string
	RefToMeta  map[string]core.FileMeta
	ChildRefs  map[string][]string
}

// LsSnapshotManager lists the file tree of a single snapshot.
type LsSnapshotManager struct {
	store store.ObjectStore
	tree  *hamt.Tree
}

func NewLsSnapshotManager(s store.ObjectStore) *LsSnapshotManager {
	return &LsSnapshotManager{
		store: s,
		tree:  hamt.NewTree(s),
	}
}

// Run resolves the snapshot, collects metadata, and returns the tree structure.
func (lm *LsSnapshotManager) Run(ctx context.Context, snapshotID string, opts ...LsSnapshotOption) (*LsSnapshotResult, error) {
	snap, ref, err := lm.resolveSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, err
	}

	refToMeta, err := lm.collectMeta(ctx, snap.Root)
	if err != nil {
		return nil, err
	}

	roots, children := lm.buildHierarchy(refToMeta)

	return &LsSnapshotResult{
		Ref:       ref,
		Snapshot:  *snap,
		RootRefs:  roots,
		RefToMeta: refToMeta,
		ChildRefs: children,
	}, nil
}

// ---------------------------------------------------------------------------
// Snapshot resolution
// ---------------------------------------------------------------------------

func (lm *LsSnapshotManager) resolveSnapshot(ctx context.Context, id string) (*core.Snapshot, string, error) {
	ref := id
	if ref == "latest" || ref == "" {
		data, err := lm.store.Get(ctx, "index/latest")
		if err != nil {
			return nil, "", fmt.Errorf("get latest index: %w", err)
		}
		var idx core.Index
		if err := json.Unmarshal(data, &idx); err != nil {
			return nil, "", fmt.Errorf("parse index: %w", err)
		}
		ref = idx.LatestSnapshot
	} else if !strings.HasPrefix(ref, "snapshot/") {
		ref = "snapshot/" + ref
	}

	data, err := lm.store.Get(ctx, ref)
	if err != nil {
		return nil, "", fmt.Errorf("load snapshot %s: %w", ref, err)
	}
	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, "", fmt.Errorf("parse snapshot: %w", err)
	}
	return &snap, ref, nil
}

// ---------------------------------------------------------------------------
// Metadata collection
// ---------------------------------------------------------------------------

func (lm *LsSnapshotManager) collectMeta(ctx context.Context, root string) (map[string]core.FileMeta, error) {
	var refs []string
	err := lm.tree.Walk(root, func(_, valueRef string) error {
		refs = append(refs, valueRef)
		return nil
	})
	if err != nil {
		return nil, err
	}

	const workers = 20
	type result struct {
		ref  string
		meta core.FileMeta
		err  error
	}

	jobs := make(chan string, len(refs))
	results := make(chan result, len(refs))

	var wg sync.WaitGroup
	for range min(workers, len(refs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range jobs {
				data, err := lm.store.Get(ctx, ref)
				if err != nil {
					results <- result{err: err}
					continue
				}
				var fm core.FileMeta
				if err := json.Unmarshal(data, &fm); err != nil {
					results <- result{err: err}
					continue
				}
				results <- result{ref: ref, meta: fm}
			}
		}()
	}

	for _, ref := range refs {
		jobs <- ref
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	refToMeta := make(map[string]core.FileMeta, len(refs))
	for res := range results {
		if res.err != nil {
			return nil, res.err
		}
		refToMeta[res.ref] = res.meta
	}
	return refToMeta, nil
}

// buildHierarchy returns sorted root refs and a parent->children map.
// Parents in FileMeta contain FileIDs, so we build a FileID->ref lookup first.
func (lm *LsSnapshotManager) buildHierarchy(refToMeta map[string]core.FileMeta) (roots []string, children map[string][]string) {
	idToRef := make(map[string]string, len(refToMeta))
	for ref, meta := range refToMeta {
		idToRef[meta.FileID] = ref
	}

	children = make(map[string][]string)

	for ref, meta := range refToMeta {
		if len(meta.Parents) == 0 {
			roots = append(roots, ref)
			continue
		}
		parentID := meta.Parents[0]
		if parentRef, ok := idToRef[parentID]; ok {
			children[parentRef] = append(children[parentRef], ref)
		} else {
			roots = append(roots, ref)
		}
	}

	sort.Slice(roots, func(i, j int) bool {
		return refToMeta[roots[i]].Name < refToMeta[roots[j]].Name
	})
	return roots, children
}
