package engine

import (
	"context"
	"sort"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

var defaultLsLog = logger.New("ls", logger.ColorCyan)

// LsSnapshotOption configures an ls-snapshot operation.
type LsSnapshotOption func(*lsSnapshotConfig)

type lsSnapshotConfig struct {
}

// LsSnapshotResult holds the data returned by an ls-snapshot operation.
type LsSnapshotResult struct {
	Ref       string
	Snapshot  core.Snapshot
	RootRefs  []string
	RefToMeta map[string]core.FileMeta
	ChildRefs map[string][]string
}

// LsSnapshotManager lists the file tree of a single snapshot.
//
// Unlike DiffManager and PruneManager its loader does not memoize, because it
// could never get a hit: a HAMT key is derived from meta.FileID, which is
// itself a FileMeta field, so no two keys share a filemeta ref. Walking a
// single root therefore reaches every ref exactly once.
type LsSnapshotManager struct {
	store store.ObjectStore
	tree  *hamt.Tree
	metas *metaLoader
	// log is where progress detail goes; see DiffManager.log.
	log *logger.Logger
}

func NewLsSnapshotManager(d Deps) *LsSnapshotManager {
	return &LsSnapshotManager{
		store: d.Store,
		log:   defaultLsLog.To(d.LogSink),
		tree:  hamt.NewTree(d.Store),
		metas: newUncachedMetaLoader(d.Store),
	}
}

// Run resolves the snapshot, collects metadata, and returns the tree structure.
func (lm *LsSnapshotManager) Run(ctx context.Context, snapshotID string, opts ...LsSnapshotOption) (*LsSnapshotResult, error) {
	var cfg lsSnapshotConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	lm.log.Debugf("Resolving snapshot %q...", snapshotID)
	snap, ref, err := lm.resolveSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	lm.log.Debugf("Resolved to %s (created %s, root %s)", ref, snap.Created, snap.Root)

	refToMeta, err := lm.collectMeta(ctx, snap.Root)
	if err != nil {
		return nil, err
	}
	var files, dirs int
	for _, m := range refToMeta {
		if m.Type == core.FileTypeFolder {
			dirs++
		} else {
			files++
		}
	}
	lm.log.Debugf("Collected %d files, %d directories", files, dirs)

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
	ref, err := resolveSnapshotRef(ctx, lm.store, id)
	if err != nil {
		return nil, "", err
	}

	snap, err := loadSnapshotByRef(ctx, lm.store, ref)
	if err != nil {
		return nil, "", err
	}
	return snap, ref, nil
}

// ---------------------------------------------------------------------------
// Metadata collection
// ---------------------------------------------------------------------------

func (lm *LsSnapshotManager) collectMeta(ctx context.Context, root string) (map[string]core.FileMeta, error) {
	refToMeta := make(map[string]core.FileMeta)
	err := walkEntriesBatched(ctx, lm.tree, root, func(entries []treeEntry) error {
		refs := make([]string, len(entries))
		for i, e := range entries {
			refs[i] = e.ref
		}
		return readGrouped(ctx, lm.store, refs, func(ref string) error {
			fm, err := lm.metas.load(ctx, ref)
			if err != nil {
				return err
			}
			refToMeta[ref] = *fm
			return nil
		})
	})
	return refToMeta, err
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
