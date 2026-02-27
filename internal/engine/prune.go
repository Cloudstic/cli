package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/internal/ui"
)

// PruneOption configures a prune operation.
type PruneOption func(*pruneConfig)

type pruneConfig struct {
	dryRun  bool
	verbose bool
}

// WithPruneDryRun shows what would be deleted without actually deleting.
func WithPruneDryRun() PruneOption {
	return func(cfg *pruneConfig) { cfg.dryRun = true }
}

// WithPruneVerbose logs each deleted key during sweep.
func WithPruneVerbose() PruneOption {
	return func(cfg *pruneConfig) { cfg.verbose = true }
}

// PruneResult holds statistics from a prune operation.
type PruneResult struct {
	BytesReclaimed int64
	ObjectsDeleted int
	ObjectsScanned int
	DryRun         bool
}

// objectPrefixes lists every key-space that prune should sweep.
var objectPrefixes = []string{"chunk/", "content/", "filemeta/", "node/", "snapshot/"}

// PruneManager implements mark-and-sweep garbage collection over the object
// store. It walks all live index → snapshot → HAMT → filemeta → content →
// chunk chains, then deletes any object not reachable from that set.
type PruneManager struct {
	store    *store.MeteredStore
	tree     *hamt.Tree
	reporter ui.Reporter
}

func NewPruneManager(s store.ObjectStore, reporter ui.Reporter) *PruneManager {
	meteredStore := store.NewMeteredStore(s)
	return &PruneManager{
		store:    meteredStore,
		tree:     hamt.NewTree(meteredStore),
		reporter: reporter,
	}
}

// Run performs a full mark-and-sweep garbage collection.
func (pm *PruneManager) Run(ctx context.Context, opts ...PruneOption) (*PruneResult, error) {
	var cfg pruneConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	markPhase := pm.reporter.StartPhase("Marking reachable objects", 0, false)
	reachable, err := pm.mark(ctx, markPhase)
	if err != nil {
		markPhase.Error()
		return nil, err
	}
	markPhase.Done()

	result := pm.sweep(ctx, reachable, &cfg)
	return result, nil
}

// ---------------------------------------------------------------------------
// Mark phase
// ---------------------------------------------------------------------------

// mark returns the set of all reachable object keys.
func (pm *PruneManager) mark(ctx context.Context, phase ui.Phase) (map[string]bool, error) {
	reachable := make(map[string]bool)

	snapRefs, err := pm.collectSnapshots(ctx, reachable)
	if err != nil {
		return nil, err
	}

	phase.Log(fmt.Sprintf("Found %d unique snapshots", len(snapRefs)))

	for ref := range snapRefs {
		if err := pm.markSnapshot(ctx, ref, reachable); err != nil {
			return nil, fmt.Errorf("mark snapshot %s: %w", ref, err)
		}
		phase.Increment(1)
	}

	return reachable, nil
}

// collectSnapshots lists all snapshot/ keys and marks them as reachable.
// It also marks index/latest as reachable so the sweep phase won't delete it.
func (pm *PruneManager) collectSnapshots(ctx context.Context, reachable map[string]bool) (map[string]bool, error) {
	keys, err := pm.store.List(ctx, "snapshot/")
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	snapRefs := make(map[string]bool, len(keys))
	for _, key := range keys {
		snapRefs[key] = true
	}

	if exists, _ := pm.store.Exists(ctx, "index/latest"); exists {
		reachable["index/latest"] = true
	}
	if exists, _ := pm.store.Exists(ctx, "index/snapshots"); exists {
		reachable["index/snapshots"] = true
	}

	return snapRefs, nil
}

func (pm *PruneManager) markSnapshot(ctx context.Context, ref string, reachable map[string]bool) error {
	if reachable[ref] {
		return nil
	}
	reachable[ref] = true

	snap, err := pm.loadSnapshot(ctx, ref)
	if err != nil {
		return err
	}

	if err := pm.tree.NodeRefs(snap.Root, func(r string) error {
		reachable[r] = true
		return nil
	}); err != nil {
		return err
	}

	return pm.tree.Walk(snap.Root, func(_, valueRef string) error {
		return pm.markFileMeta(ctx, valueRef, reachable)
	})
}

func (pm *PruneManager) markFileMeta(ctx context.Context, ref string, reachable map[string]bool) error {
	if reachable[ref] {
		return nil
	}
	reachable[ref] = true

	meta, err := pm.loadMeta(ctx, ref)
	if err != nil {
		return err
	}

	if meta.ContentHash == "" {
		return nil
	}
	return pm.markContent(ctx, "content/"+meta.ContentHash, reachable)
}

func (pm *PruneManager) markContent(ctx context.Context, ref string, reachable map[string]bool) error {
	if reachable[ref] {
		return nil
	}
	reachable[ref] = true

	content, err := pm.loadContent(ctx, ref)
	if err != nil {
		return err
	}

	for _, chunkRef := range content.Chunks {
		reachable[chunkRef] = true
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sweep phase
// ---------------------------------------------------------------------------

func (pm *PruneManager) sweep(ctx context.Context, reachable map[string]bool, cfg *pruneConfig) *PruneResult {
	var totalKeys int
	for _, prefix := range objectPrefixes {
		keys, err := pm.store.List(ctx, prefix)
		if err != nil {
			continue
		}
		totalKeys += len(keys)
	}

	label := "Sweeping unreachable objects"
	if cfg.dryRun {
		label = "Scanning unreachable objects (dry run)"
	}
	phase := pm.reporter.StartPhase(label, int64(totalKeys), true)
	result := &PruneResult{DryRun: cfg.dryRun}

	for _, prefix := range objectPrefixes {
		keys, err := pm.store.List(ctx, prefix)
		if err != nil {
			continue
		}
		for _, key := range keys {
			result.ObjectsScanned++
			if reachable[key] {
				phase.Increment(0)
				continue
			}
			if cfg.dryRun {
				if cfg.verbose {
					phase.Log(fmt.Sprintf("Would delete: %s", key))
				}
				result.ObjectsDeleted++
				phase.Increment(0)
				continue
			}
			size, err := pm.store.DeleteReturnSize(ctx, key)
			if err != nil {
				continue
			}
			if cfg.verbose {
				phase.Log(fmt.Sprintf("Deleted: %s", key))
			}
			result.ObjectsDeleted++
			phase.Increment(size)
		}
	}
	if !cfg.dryRun {
		result.BytesReclaimed = -pm.store.BytesWritten()
		pm.store.Reset()
	}
	phase.Done()
	return result
}

// ---------------------------------------------------------------------------
// Loaders
// ---------------------------------------------------------------------------

func (pm *PruneManager) loadSnapshot(ctx context.Context, ref string) (*core.Snapshot, error) {
	data, err := pm.store.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("get snapshot %s: %w", ref, err)
	}
	var s core.Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (pm *PruneManager) loadMeta(ctx context.Context, ref string) (*core.FileMeta, error) {
	data, err := pm.store.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("get filemeta %s: %w", ref, err)
	}
	var fm core.FileMeta
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil, err
	}
	return &fm, nil
}

func (pm *PruneManager) loadContent(ctx context.Context, ref string) (*core.Content, error) {
	data, err := pm.store.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("get content %s: %w", ref, err)
	}
	var c core.Content
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
