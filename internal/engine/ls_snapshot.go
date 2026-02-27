package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
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
	store     store.ObjectStore
	tree      *hamt.Tree
	metaCache map[string]core.FileMeta
}

func NewLsSnapshotManager(s store.ObjectStore) *LsSnapshotManager {
	return &LsSnapshotManager{
		store: s,
		tree:  NewCachedTree(s),
	}
}

// Run resolves the snapshot, collects metadata, and returns the tree structure.
func (lm *LsSnapshotManager) Run(ctx context.Context, snapshotID string, opts ...LsSnapshotOption) (*LsSnapshotResult, error) {
	lm.metaCache, _ = LoadFileMetaCache(lm.store)

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
	refToMeta := make(map[string]core.FileMeta)
	err := lm.tree.Walk(root, func(_, valueRef string) error {
		fm, err := lm.loadMeta(ctx, valueRef)
		if err != nil {
			return err
		}
		refToMeta[valueRef] = *fm
		return nil
	})
	return refToMeta, err
}

func (lm *LsSnapshotManager) loadMeta(ctx context.Context, ref string) (*core.FileMeta, error) {
	if fm, ok := lm.metaCache[ref]; ok {
		return &fm, nil
	}
	debugf(dYellow+"cache miss"+dReset+" loadMeta %s", ref)
	data, err := lm.store.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	var fm core.FileMeta
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil, err
	}
	return &fm, nil
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
