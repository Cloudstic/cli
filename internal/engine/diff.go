package engine

import (
	"context"
	"sort"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

var defaultDiffLog = logger.New("diff", logger.ColorCyan)

// ChangeType describes how a file differs between two snapshots.
type ChangeType string

const (
	ChangeAdded    ChangeType = "A"
	ChangeRemoved  ChangeType = "D"
	ChangeModified ChangeType = "M"
)

// FileChange is a single entry in a diff report.
type FileChange struct {
	Type ChangeType
	Path string
	Meta core.FileMeta
}

// DiffOption configures a diff operation.
type DiffOption func(*diffConfig)

type diffConfig struct {
}

// DiffResult holds the outcome of a diff operation.
type DiffResult struct {
	Ref1    string
	Ref2    string
	Changes []FileChange
}

// DiffManager compares two snapshots and reports file-level changes.
type DiffManager struct {
	store store.ObjectStore
	tree  *hamt.Tree
	metas *metaLoader
	// log is where progress detail goes. It used to be written straight to
	// os.Stderr, which a library caller could neither capture nor silence.
	log *logger.Logger
}

func NewDiffManager(d Deps) *DiffManager {
	return &DiffManager{
		log:   defaultDiffLog.To(d.LogSink),
		store: d.Store,
		tree:  hamt.NewTree(d.Store),
		metas: newMetaLoader(d.Store),
	}
}

// Run resolves two snapshot IDs and computes the diff.
func (dm *DiffManager) Run(ctx context.Context, snapID1, snapID2 string, opts ...DiffOption) (*DiffResult, error) {
	var cfg diffConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	dm.log.Debugf("Resolving snapshot %q...", snapID1)
	root1, ref1, err := dm.loadRoot(ctx, snapID1)
	if err != nil {
		return nil, err
	}
	dm.log.Debugf("Resolving snapshot %q...", snapID2)
	root2, ref2, err := dm.loadRoot(ctx, snapID2)
	if err != nil {
		return nil, err
	}

	dm.log.Debugf("Computing diff between %s and %s...", ref1, ref2)
	changes, err := dm.diffRoots(ctx, root1, root2)
	if err != nil {
		return nil, err
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Path < changes[j].Path
	})

	var added, removed, modified int
	for _, c := range changes {
		switch c.Type {
		case ChangeAdded:
			added++
		case ChangeRemoved:
			removed++
		case ChangeModified:
			modified++
		}
		dm.log.Debugf("Found %d changes: %d added, %d removed, %d modified", len(changes), added, removed, modified)
	}

	return &DiffResult{Ref1: ref1, Ref2: ref2, Changes: changes}, nil
}

// ---------------------------------------------------------------------------
// Snapshot resolution
// ---------------------------------------------------------------------------

// loadRoot resolves a snapshot ID and returns its HAMT root along with the
// fully-qualified snapshot ref (for display).
func (dm *DiffManager) loadRoot(ctx context.Context, id string) (root, ref string, err error) {
	ref, err = dm.resolveSnapshot(ctx, id)
	if err != nil {
		return "", "", err
	}
	snap, err := dm.loadSnapshot(ctx, ref)
	if err != nil {
		return "", "", err
	}
	return snap.Root, ref, nil
}

func (dm *DiffManager) resolveSnapshot(ctx context.Context, id string) (string, error) {
	return resolveSnapshotRef(ctx, dm.store, id)
}

func (dm *DiffManager) loadSnapshot(ctx context.Context, ref string) (*core.Snapshot, error) {
	return loadSnapshotByRef(ctx, dm.store, ref)
}

// ---------------------------------------------------------------------------
// Diff logic
// ---------------------------------------------------------------------------

func (dm *DiffManager) diffRoots(ctx context.Context, root1, root2 string) ([]FileChange, error) {
	var changes []FileChange
	oldByID, err := dm.collectMetadata(ctx, root1)
	if err != nil {
		return nil, err
	}
	newByID, err := dm.collectMetadata(ctx, root2)
	if err != nil {
		return nil, err
	}

	err = dm.tree.Diff(ctx, root1, root2, func(d hamt.DiffEntry) error {
		change, err := dm.toFileChange(ctx, d, oldByID, newByID)
		if err != nil {
			return err
		}
		changes = append(changes, change)
		return nil
	})
	return changes, err
}

func (dm *DiffManager) toFileChange(ctx context.Context, d hamt.DiffEntry, oldByID, newByID map[string]core.FileMeta) (FileChange, error) {
	ct, metaRef := classifyEntry(d)

	meta, err := dm.metas.load(ctx, metaRef)
	if err != nil {
		return FileChange{}, err
	}
	byID := newByID
	if ct == ChangeRemoved {
		byID = oldByID
	}
	return FileChange{
		Type: ct,
		Path: fileMetaPath(*meta, func(parentID string) (core.FileMeta, bool) {
			parent, ok := byID[parentID]
			return parent, ok
		}),
		Meta: *meta,
	}, nil
}

func classifyEntry(d hamt.DiffEntry) (ChangeType, string) {
	switch {
	case d.OldValue == "":
		return ChangeAdded, d.NewValue
	case d.NewValue == "":
		return ChangeRemoved, d.OldValue
	default:
		return ChangeModified, d.NewValue
	}
}

func (dm *DiffManager) collectMetadata(ctx context.Context, root string) (map[string]core.FileMeta, error) {
	byID := make(map[string]core.FileMeta)
	err := dm.tree.Walk(ctx, root, func(_, valueRef string) error {
		fm, err := dm.metas.load(ctx, valueRef)
		if err != nil {
			return err
		}
		byID[fm.FileID] = *fm
		return nil
	})
	return byID, err
}
