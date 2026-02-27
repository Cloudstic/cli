package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/internal/ui"
)

// backupStats holds atomic counters accumulated during a backup run.
type backupStats struct {
	filesNew        atomic.Int64
	filesChanged    atomic.Int64
	filesUnmodified atomic.Int64
	filesRemoved    atomic.Int64
	dirsNew         atomic.Int64
	dirsChanged     atomic.Int64
	dirsUnmodified  atomic.Int64
	dirsRemoved     atomic.Int64
	startTime       time.Time
}

// BackupOption configures a backup operation.
type BackupOption func(*backupConfig)

type backupConfig struct {
	verbose   bool
	dryRun    bool
	tags      []string
	generator string
	meta      map[string]string
}

// WithBackupDryRun scans the source and reports what would change without writing to the store.
func WithBackupDryRun() BackupOption {
	return func(cfg *backupConfig) { cfg.dryRun = true }
}

// WithVerbose enables verbose output during backup.
func WithVerbose() BackupOption {
	return func(cfg *backupConfig) { cfg.verbose = true }
}

// WithTags adds tags to the backup snapshot.
func WithTags(tags ...string) BackupOption {
	return func(cfg *backupConfig) { cfg.tags = append(cfg.tags, tags...) }
}

// WithGenerator overrides the default generator name in snapshot metadata.
func WithGenerator(name string) BackupOption {
	return func(cfg *backupConfig) { cfg.generator = name }
}

// WithMeta adds a key-value pair to the snapshot metadata.
func WithMeta(key, value string) BackupOption {
	return func(cfg *backupConfig) { cfg.meta[key] = value }
}

// BackupManager orchestrates a backup: scanning a source for changes, uploading
// new or modified files, and persisting a snapshot backed by a Merkle-HAMT.
type BackupManager struct {
	source     store.Source
	store      store.ObjectStore
	keyCache   *store.KeyCacheStore
	tree       *hamt.Tree
	cache      *hamt.TransactionalStore
	chunker    *Chunker
	reporter   ui.Reporter
	stats      *backupStats
	sourceInfo core.SourceInfo
	cfg        backupConfig

	newMetas     map[string]core.FileMeta
	metaCache    map[string]core.FileMeta
	contentCache ContentCatalog
	pendingMetas map[string][]byte // deferred filemeta PUTs (ref → JSON)
}

func NewBackupManager(src store.Source, dest store.ObjectStore, reporter ui.Reporter, opts ...BackupOption) *BackupManager {
	cfg := backupConfig{
		generator: "cloudstic-cli",
		meta:      map[string]string{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	sourceInfo := src.Info()
	keyCache := store.NewKeyCacheStore(dest)
	cache := hamt.NewTransactionalStore(keyCache)
	return &BackupManager{
		source:       src,
		store:        keyCache,
		keyCache:     keyCache,
		tree:         hamt.NewTree(cache),
		cache:        cache,
		chunker:      NewChunker(keyCache),
		reporter:     reporter,
		sourceInfo:   sourceInfo,
		cfg:          cfg,
		newMetas:     make(map[string]core.FileMeta),
		contentCache: LoadContentCache(dest),
		pendingMetas: make(map[string][]byte),
	}
}

// RunResult holds the outcome of a successful backup run.
type RunResult struct {
	SnapshotHash     string
	SnapshotRef      string
	Root             string
	FilesNew         int64
	FilesChanged     int64
	FilesUnmodified  int64
	FilesRemoved     int64
	DirsNew          int64
	DirsChanged      int64
	DirsUnmodified   int64
	DirsRemoved      int64
	BytesAddedRaw    int64
	BytesAddedStored int64
	Duration         time.Duration
	DryRun           bool
}

// Run executes a full backup: scan the source for changes, upload new/modified
// files, build a new HAMT root, and persist a snapshot.
func (bm *BackupManager) Run(ctx context.Context) (*RunResult, error) {
	bm.metaCache, _ = LoadFileMetaCache(bm.store)
	bm.cache.PreloadReadCache(LoadNodeCache(bm.store))

	seq := bm.loadLatestSeq()
	prevSnap := bm.findPreviousSnapshot(bm.sourceInfo)
	bm.stats = &backupStats{startTime: time.Now()}

	var oldRoot string
	var changeToken string
	if prevSnap != nil {
		oldRoot = prevSnap.Root
		changeToken = prevSnap.ChangeToken
	}

	newRoot, pending, totalBytes, newToken, usedFullScan, err := bm.scanSource(ctx, oldRoot, changeToken)
	if err != nil {
		return nil, err
	}

	if bm.cfg.dryRun {
		if usedFullScan {
			if err := bm.countRemoved(oldRoot, newRoot); err != nil {
				return nil, fmt.Errorf("counting removed entries: %w", err)
			}
		}
		r := bm.buildResult()
		r.Root = newRoot
		r.DryRun = true
		return r, nil
	}

	if err := bm.flushPendingMetas(ctx); err != nil {
		return nil, err
	}

	if err := bm.keyCache.PreloadKeys(ctx, "chunk/", "content/", "node/"); err != nil {
		return nil, fmt.Errorf("preload key cache: %w", err)
	}

	newRoot, err = bm.upload(ctx, pending, totalBytes, newRoot)
	if err != nil {
		return nil, err
	}

	if usedFullScan {
		if err := bm.countRemoved(oldRoot, newRoot); err != nil {
			return nil, fmt.Errorf("counting removed entries: %w", err)
		}
	}

	snapRef, snapHash, err := bm.saveSnapshot(ctx, newRoot, seq+1, newToken)
	if err != nil {
		return nil, err
	}

	if err := bm.cache.Flush(newRoot); err != nil {
		return nil, fmt.Errorf("flush hamt nodes: %w", err)
	}

	_ = SaveNodeCache(bm.store, bm.cache.ExportCaches())
	_ = SaveContentCache(bm.store, bm.contentCache)

	r := bm.buildResult()
	r.SnapshotHash = snapHash
	r.SnapshotRef = snapRef
	r.Root = newRoot
	return r, nil
}

func (bm *BackupManager) buildResult() *RunResult {
	return &RunResult{
		FilesNew:        bm.stats.filesNew.Load(),
		FilesChanged:    bm.stats.filesChanged.Load(),
		FilesUnmodified: bm.stats.filesUnmodified.Load(),
		FilesRemoved:    bm.stats.filesRemoved.Load(),
		DirsNew:         bm.stats.dirsNew.Load(),
		DirsChanged:     bm.stats.dirsChanged.Load(),
		DirsUnmodified:  bm.stats.dirsUnmodified.Load(),
		DirsRemoved:     bm.stats.dirsRemoved.Load(),
		Duration:        time.Since(bm.stats.startTime),
	}
}

// loadLatestSeq returns the global sequence number from the most recent
// snapshot. On a fresh repository it returns 0.
func (bm *BackupManager) loadLatestSeq() int {
	_, seq := resolveLatest(bm.store)
	return seq
}

// findPreviousSnapshot lists all snapshots and returns the most recent one
// whose Source matches the given info (Type + Account + Path).
// Returns nil when no matching snapshot exists.
func (bm *BackupManager) findPreviousSnapshot(info core.SourceInfo) *core.Snapshot {
	entries, err := ListAllSnapshots(bm.store)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Snap.Source != nil &&
			e.Snap.Source.Type == info.Type &&
			e.Snap.Source.Account == info.Account &&
			e.Snap.Source.Path == info.Path {
			snap := e.Snap
			return &snap
		}
	}
	return nil
}

func (bm *BackupManager) saveSnapshot(ctx context.Context, root string, seq int, changeToken string) (ref, hash string, err error) {
	meta := make(map[string]string, len(bm.cfg.meta)+1)
	for k, v := range bm.cfg.meta {
		meta[k] = v
	}
	meta["generator"] = bm.cfg.generator

	snap := core.Snapshot{
		Version:     1,
		Created:     time.Now().Format(time.RFC3339),
		Root:        root,
		Seq:         seq,
		Source:      &bm.sourceInfo,
		Tags:        bm.cfg.tags,
		Meta:        meta,
		ChangeToken: changeToken,
	}

	hash, snapData, err := core.ComputeJSONHash(&snap)
	if err != nil {
		return "", "", err
	}

	ref = "snapshot/" + hash
	if err := bm.store.Put(ctx, ref, snapData); err != nil {
		return "", "", err
	}

	if err := updateLatest(bm.store, ref, seq); err != nil {
		return "", "", err
	}

	_ = AddSnapshotToIndex(bm.store, snap, ref)
	_ = AddFileMetasToIndex(bm.store, bm.newMetas)

	return ref, hash, nil
}

func (bm *BackupManager) trackFileMeta(ref string, fm core.FileMeta) {
	bm.newMetas[ref] = fm
}

func (bm *BackupManager) loadMeta(ref string) (*core.FileMeta, error) {
	if fm, ok := bm.metaCache[ref]; ok {
		return &fm, nil
	}
	debugf(dYellow+"cache miss"+dReset+" loadMeta %s", ref)
	data, err := bm.store.Get(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	var fm core.FileMeta
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil, err
	}
	return &fm, nil
}
