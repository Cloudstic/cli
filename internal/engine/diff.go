package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

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
	verbose bool
}

// WithDiffVerbose enables verbose output for the diff operation.
func WithDiffVerbose() DiffOption {
	return func(cfg *diffConfig) { cfg.verbose = true }
}

// DiffResult holds the outcome of a diff operation.
type DiffResult struct {
	Ref1    string
	Ref2    string
	Changes []FileChange
}

// DiffManager compares two snapshots and reports file-level changes.
type DiffManager struct {
	store     store.ObjectStore
	tree      *hamt.Tree
	metaCache map[string]core.FileMeta
}

func NewDiffManager(s store.ObjectStore) *DiffManager {
	return &DiffManager{
		store: s,
		tree:  hamt.NewTree(hamt.NewTransactionalStore(s)),
	}
}

// Run resolves two snapshot IDs and computes the diff.
func (dm *DiffManager) Run(ctx context.Context, snapID1, snapID2 string, opts ...DiffOption) (*DiffResult, error) {
	var cfg diffConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	dm.metaCache = make(map[string]core.FileMeta)

	if cfg.verbose {
		fmt.Fprintf(os.Stderr, "Resolving snapshot %q...\n", snapID1)
	}
	root1, ref1, err := dm.loadRoot(ctx, snapID1)
	if err != nil {
		return nil, err
	}
	if cfg.verbose {
		fmt.Fprintf(os.Stderr, "Resolving snapshot %q...\n", snapID2)
	}
	root2, ref2, err := dm.loadRoot(ctx, snapID2)
	if err != nil {
		return nil, err
	}

	if cfg.verbose {
		fmt.Fprintf(os.Stderr, "Computing diff between %s and %s...\n", ref1, ref2)
	}
	changes, err := dm.diffRoots(root1, root2)
	if err != nil {
		return nil, err
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Path < changes[j].Path
	})

	if cfg.verbose {
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
		}
		fmt.Fprintf(os.Stderr, "Found %d changes: %d added, %d removed, %d modified\n", len(changes), added, removed, modified)
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
	if id == "latest" || id == "" {
		data, err := dm.store.Get(ctx, "index/latest")
		if err != nil {
			return "", fmt.Errorf("get latest index: %w", err)
		}
		var idx core.Index
		if err := json.Unmarshal(data, &idx); err != nil {
			return "", err
		}
		return idx.LatestSnapshot, nil
	}
	if !strings.HasPrefix(id, "snapshot/") {
		return "snapshot/" + id, nil
	}
	return id, nil
}

func (dm *DiffManager) loadSnapshot(ctx context.Context, ref string) (*core.Snapshot, error) {
	data, err := dm.store.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("load snapshot %s: %w", ref, err)
	}
	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// ---------------------------------------------------------------------------
// Diff logic
// ---------------------------------------------------------------------------

func (dm *DiffManager) diffRoots(root1, root2 string) ([]FileChange, error) {
	var changes []FileChange
	oldByID, err := dm.collectMetadata(root1)
	if err != nil {
		return nil, err
	}
	newByID, err := dm.collectMetadata(root2)
	if err != nil {
		return nil, err
	}

	err = dm.tree.Diff(root1, root2, func(d hamt.DiffEntry) error {
		change, err := dm.toFileChange(d, oldByID, newByID)
		if err != nil {
			return err
		}
		changes = append(changes, change)
		return nil
	})
	return changes, err
}

func (dm *DiffManager) toFileChange(d hamt.DiffEntry, oldByID, newByID map[string]core.FileMeta) (FileChange, error) {
	ct, metaRef := classifyEntry(d)

	meta, err := dm.loadMeta(metaRef)
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

func (dm *DiffManager) loadMeta(ref string) (*core.FileMeta, error) {
	if dm.metaCache == nil {
		dm.metaCache = make(map[string]core.FileMeta)
	}
	if fm, ok := dm.metaCache[ref]; ok {
		return &fm, nil
	}
	data, err := dm.store.Get(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	var fm core.FileMeta
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil, err
	}
	dm.metaCache[ref] = fm
	return &fm, nil
}

func (dm *DiffManager) collectMetadata(root string) (map[string]core.FileMeta, error) {
	byID := make(map[string]core.FileMeta)
	err := dm.tree.Walk(root, func(_, valueRef string) error {
		fm, err := dm.loadMeta(valueRef)
		if err != nil {
			return err
		}
		byID[fm.FileID] = *fm
		return nil
	})
	return byID, err
}
