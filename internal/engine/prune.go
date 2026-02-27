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
	store        *store.MeteredStore
	tree         *hamt.Tree
	reporter     ui.Reporter
	metaCache    map[string]core.FileMeta
	contentCache ContentCatalog
}

func NewPruneManager(s store.ObjectStore, reporter ui.Reporter) *PruneManager {
	meteredStore := store.NewMeteredStore(s)
	return &PruneManager{
		store:    meteredStore,
		tree:     NewCachedTree(meteredStore),
		reporter: reporter,
	}
}

func (pm *PruneManager) Run(ctx context.Context, opts ...PruneOption) (*PruneResult, error) {
	var cfg pruneConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	pm.metaCache, _ = LoadFileMetaCache(pm.store)
	pm.contentCache = LoadContentCache(pm.store)

	markPhase := pm.reporter.StartPhase("Marking reachable objects", 0, false)
	reachable, err := pm.mark(ctx, markPhase)
	if err != nil {
		markPhase.Error()
		return nil, err
	}
	markPhase.Done()

	_ = SaveContentCache(pm.store, pm.contentCache)

	result := pm.sweep(ctx, reachable, &cfg)

	if !cfg.dryRun && result.ObjectsDeleted > 0 {
		pm.rebuildCaches(reachable)
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
	if exists, _ := pm.store.Exists(ctx, "index/snapshots"); exists {
		reachable["index/snapshots"] = true
	}
	if exists, _ := pm.store.Exists(ctx, "index/filemeta"); exists {
		reachable["index/filemeta"] = true
	}
	if exists, _ := pm.store.Exists(ctx, "index/nodes"); exists {
		reachable["index/nodes"] = true
	}
	if exists, _ := pm.store.Exists(ctx, "index/content"); exists {
		reachable["index/content"] = true
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
		return pm.markContent(ctx, "content/"+meta.ContentHash, reachable)
	}
	return nil
}

func (pm *PruneManager) markContent(ctx context.Context, ref string, reachable map[string]bool) error {
	if reachable[ref] {
		return nil
	}
	reachable[ref] = true

	if chunks, ok := pm.contentCache[ref]; ok {
		for _, c := range chunks {
			reachable[c] = true
		}
		return nil
	}

	debugf(dYellow+"cache miss"+dReset+" loadContent %s", ref)
	data, err := pm.store.Get(ctx, ref)
	if err != nil {
		return fmt.Errorf("get content %s: %w", ref, err)
	}
	var content core.Content
	if err := json.Unmarshal(data, &content); err != nil {
		return err
	}

	pm.contentCache[ref] = content.Chunks
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

func (pm *PruneManager) rebuildCaches(reachable map[string]bool) {
	if len(pm.metaCache) > 0 {
		pruned := 0
		for ref := range pm.metaCache {
			if !reachable[ref] {
				delete(pm.metaCache, ref)
				pruned++
			}
		}
		if pruned > 0 {
			debugf("pruned %d stale entries from filemeta cache", pruned)
			_ = saveFileMetaCatalog(pm.store, pm.metaCache)
		}
	}

	nodeData := LoadNodeCache(pm.store)
	if len(nodeData) > 0 {
		pruned := 0
		for ref := range nodeData {
			if !reachable[ref] {
				delete(nodeData, ref)
				pruned++
			}
		}
		if pruned > 0 {
			debugf("pruned %d stale entries from node cache", pruned)
			_ = SaveNodeCache(pm.store, nodeData)
		}
	}

	if len(pm.contentCache) > 0 {
		pruned := 0
		for ref := range pm.contentCache {
			if !reachable[ref] {
				delete(pm.contentCache, ref)
				pruned++
			}
		}
		if pruned > 0 {
			debugf("pruned %d stale entries from content cache", pruned)
			_ = SaveContentCache(pm.store, pm.contentCache)
		}
	}
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
	debugf(dYellow+"cache miss"+dReset+" loadMeta %s", ref)
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

