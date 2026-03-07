package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

type PruneOption func(*pruneConfig)

type pruneConfig struct {
	dryRun  bool
	verbose bool
}

func WithPruneDryRun() PruneOption {
	return func(cfg *pruneConfig) { cfg.dryRun = true }
}

func WithPruneVerbose() PruneOption {
	return func(cfg *pruneConfig) { cfg.verbose = true }
}

type PruneResult struct {
	BytesReclaimed int64
	ObjectsDeleted int
	ObjectsScanned int
	DryRun         bool
}

var objectPrefixes = []string{"chunk/", "content/", "filemeta/", "node/", "snapshot/"}

// PruneManager implements mark-and-sweep garbage collection over the object store.
type PruneManager struct {
	store     *store.MeteredStore
	tree      *hamt.Tree
	reporter  ui.Reporter
	metaCache map[string]core.FileMeta
}

func NewPruneManager(s store.ObjectStore, reporter ui.Reporter) *PruneManager {
	meteredStore := store.NewMeteredStore(s)
	return &PruneManager{
		store:    meteredStore,
		tree:     hamt.NewTree(hamt.NewTransactionalStore(meteredStore)),
		reporter: reporter,
	}
}

func (pm *PruneManager) Run(ctx context.Context, opts ...PruneOption) (*PruneResult, error) {
	var cfg pruneConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	if !cfg.dryRun {
		lock, err := AcquireRepoLock(ctx, pm.store, "prune")
		if err != nil {
			return nil, err
		}
		defer lock.Release()
	}

	pm.metaCache = make(map[string]core.FileMeta)

	markPhase := pm.reporter.StartPhase("Marking reachable objects", 0, false)
	reachable, err := pm.mark(ctx, markPhase)
	if err != nil {
		markPhase.Error()
		return nil, err
	}
	markPhase.Done()

	result := pm.sweep(ctx, reachable, &cfg)

	if !cfg.dryRun {
		// Attempt to repack fragmented packfiles.
		// We walk down the store chain to find the PackStore if it is enabled.
		var packStore *store.PackStore
		var current store.ObjectStore = pm.store
		for current != nil {
			if ps, ok := current.(*store.PackStore); ok {
				packStore = ps
				break
			}
			if un, ok := current.(store.Unwrapper); ok {
				current = un.Unwrap()
			} else {
				break
			}
		}

		if packStore != nil {
			repackPhase := pm.reporter.StartPhase("Repacking fragmented index files", 0, false)
			// Threshold: Repack any packfile that is more than 30% empty
			bytesReclaimed, packsDeleted, err := packStore.Repack(ctx, 0.3)
			if err != nil {
				repackPhase.Error()
				return nil, fmt.Errorf("repack: %w", err)
			}
			if cfg.verbose {
				repackPhase.Log(fmt.Sprintf("Repacked/deleted %d packs, reclaimed %d bytes", packsDeleted, bytesReclaimed))
			}
			result.BytesReclaimed += bytesReclaimed
			result.ObjectsDeleted += packsDeleted
			repackPhase.Done()
		}

		_ = pm.store.Flush(ctx)
	}

	return result, nil
}

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
	if exists, _ := pm.store.Exists(ctx, "index/packs"); exists {
		reachable["index/packs"] = true
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

	if meta.ContentHash != "" {
		contentKey := meta.ContentRef
		if contentKey == "" {
			contentKey = meta.ContentHash
		}
		return pm.markContent(ctx, "content/"+contentKey, reachable)
	}
	return nil
}

func (pm *PruneManager) markContent(ctx context.Context, ref string, reachable map[string]bool) error {
	if reachable[ref] {
		return nil
	}
	reachable[ref] = true

	data, err := pm.store.Get(ctx, ref)
	if err != nil {
		return fmt.Errorf("get content %s: %w", ref, err)
	}
	var content core.Content
	if err := json.Unmarshal(data, &content); err != nil {
		return err
	}

	for _, c := range content.Chunks {
		reachable[c] = true
	}
	return nil
}

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
	if fm, ok := pm.metaCache[ref]; ok {
		return &fm, nil
	}
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
