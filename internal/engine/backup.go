package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/source"
	"github.com/cloudstic/cli/pkg/store"
)

var backupLog = logger.New("backup", logger.ColorGreen)

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
	verbose             bool
	dryRun              bool
	ignoreEmptySnapshot bool
	tags                []string
	generator           string
	meta                map[string]string
	excludeHash         string
}

// WithBackupDryRun scans the source and reports what would change without writing to the store.
func WithBackupDryRun() BackupOption {
	return func(cfg *backupConfig) { cfg.dryRun = true }
}

// WithIgnoreEmptySnapshot skips persisting a new snapshot when the resulting
// tree is identical to the previous snapshot for the same source lineage.
func WithIgnoreEmptySnapshot() BackupOption {
	return func(cfg *backupConfig) { cfg.ignoreEmptySnapshot = true }
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

// WithExcludeHash records the hash of the active exclude patterns. When this
// differs from the previous snapshot the engine forces a full rescan.
func WithExcludeHash(hash string) BackupOption {
	return func(cfg *backupConfig) { cfg.excludeHash = hash }
}

// BackupManager orchestrates a backup: scanning a source for changes, uploading
// new or modified files, and persisting a snapshot backed by a Merkle-HAMT.
type BackupManager struct {
	source     source.Source
	store      store.ObjectStore
	keyCache   *store.KeyCacheStore
	tree       *hamt.Tree
	txn        *hamt.Txn // working tree; opened by scanSource, written by Commit
	chunker    *Chunker
	reporter   ui.Reporter
	stats      *backupStats
	sourceInfo core.SourceInfo
	cfg        backupConfig

	newMetas     map[string]core.FileMeta
	metaCacheMu  sync.RWMutex
	metaCache    map[string]core.FileMeta
	pendingMetas map[string][]byte // deferred filemeta PUTs (ref → JSON)
	parentIndex  map[string]string // fileID → primary parent fileID (for AffinityKey lookups)
	hmacKey      []byte
}

func NewBackupManager(src source.Source, dest store.ObjectStore, reporter ui.Reporter, hmacKey []byte, opts ...BackupOption) *BackupManager {
	cfg := backupConfig{
		generator: "cloudstic-cli",
		meta:      map[string]string{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	sourceInfo := src.Info()
	keyCache := store.NewKeyCacheStore(dest)
	return &BackupManager{
		source:       src,
		store:        keyCache,
		keyCache:     keyCache,
		tree:         hamt.NewTree(keyCache),
		chunker:      NewChunker(keyCache, hmacKey),
		reporter:     reporter,
		sourceInfo:   sourceInfo,
		cfg:          cfg,
		newMetas:     make(map[string]core.FileMeta),
		metaCache:    make(map[string]core.FileMeta),
		pendingMetas: make(map[string][]byte),
		parentIndex:  make(map[string]string),
		hmacKey:      hmacKey,
	}
}

// RunResult holds the outcome of a successful backup run.
type RunResult struct {
	SnapshotHash         string
	SnapshotRef          string
	Root                 string
	FilesNew             int64
	FilesChanged         int64
	FilesUnmodified      int64
	FilesRemoved         int64
	DirsNew              int64
	DirsChanged          int64
	DirsUnmodified       int64
	DirsRemoved          int64
	BytesAddedRaw        int64
	BytesAddedStored     int64
	Duration             time.Duration
	DryRun               bool
	EmptySnapshotIgnored bool
}

// Run executes a full backup: scan the source for changes, upload new/modified
// files, build a new HAMT root, and persist a snapshot.
func (bm *BackupManager) Run(ctx context.Context) (*RunResult, error) {
	if !bm.cfg.dryRun {
		lock, lockedCtx, err := AcquireSharedLock(ctx, bm.store, "backup")
		if err != nil {
			return nil, err
		}
		defer lock.Release()
		ctx = lockedCtx
	}

	defer func() {
		if !bm.cfg.dryRun {
			_ = bm.store.Flush(ctx)
		}
	}()

	var seq int
	var prevSnap *core.Snapshot

	var g errgroup.Group

	g.Go(func() error {
		var err error
		seq, err = bm.loadLatestSeq()
		return err
	})

	g.Go(func() error {
		var err error
		prevSnap, err = bm.findPreviousSnapshot(bm.sourceInfo)
		return err
	})

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("initialization failed: %w", err)
	}

	bm.stats = &backupStats{startTime: time.Now()}

	var oldRoot string
	var changeToken string
	if prevSnap != nil {
		oldRoot = prevSnap.Root
		changeToken = prevSnap.ChangeToken
	}

	// Force a full rescan when exclude patterns changed since the last
	// snapshot. Clearing the change token makes scanSource fall through
	// to the full Walk path, which also captures a fresh token for the
	// next incremental run.
	if changeToken != "" && prevSnap != nil {
		oldHash := prevSnap.ExcludeHash
		newHash := bm.cfg.excludeHash
		if oldHash != newHash {
			backupLog.Debugf("exclude patterns changed (old=%q new=%q), forcing full rescan", oldHash, newHash)
			changeToken = ""
		} else if newHash != "" {
			backupLog.Debugf("exclude patterns unchanged (hash=%q), continuing incremental", newHash)
		}
	} else if prevSnap == nil {
		backupLog.Debugf("no previous snapshot found, running full scan")
	}

	pending, totalBytes, newToken, usedFullScan, err := bm.scanSource(ctx, oldRoot, changeToken)
	if err != nil {
		return nil, err
	}

	if bm.cfg.dryRun {
		if usedFullScan {
			if err := bm.countRemoved(ctx, oldRoot); err != nil {
				return nil, fmt.Errorf("counting removed entries: %w", err)
			}
		}
		// Root computes the ref the tree would have without writing it, which
		// is what makes a dry run genuinely read-only.
		newRoot, err := bm.txn.Root(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute hamt root: %w", err)
		}
		r := bm.buildResult()
		r.Root = newRoot
		r.DryRun = true
		return r, nil
	}

	if err := bm.flushPendingMetas(ctx); err != nil {
		return nil, err
	}

	// Wait for key cache to finish preloading from inner lists.
	if err := bm.keyCache.PreloadKeys(ctx, "chunk/", "content/", "node/"); err != nil {
		return nil, fmt.Errorf("preload key cache: %w", err)
	}

	if err := bm.upload(ctx, pending, totalBytes); err != nil {
		return nil, err
	}

	if usedFullScan {
		if err := bm.countRemoved(ctx, oldRoot); err != nil {
			return nil, fmt.Errorf("counting removed entries: %w", err)
		}
	}

	// Commit the tree before anything points at it. A snapshot naming a root
	// whose nodes are not yet written is unreadable, so the order here —
	// nodes, then snapshot, then index/latest — is load-bearing, not stylistic.
	newRoot, err := bm.txn.Commit(ctx)
	if err != nil {
		return nil, fmt.Errorf("commit hamt: %w", err)
	}

	if bm.cfg.ignoreEmptySnapshot && prevSnap != nil && newRoot == oldRoot {
		r := bm.buildResult()
		r.Root = newRoot
		r.EmptySnapshotIgnored = true
		return r, nil
	}

	newSeq := seq + 1
	snapRef, snapHash, snap, err := bm.writeSnapshotObject(ctx, newRoot, newSeq, newToken)
	if err != nil {
		return nil, err
	}

	// Both the HAMT nodes committed above and the snapshot object just
	// written are content-addressed, so PackStore may still be holding them
	// only in its in-memory buffer. index/latest is a mutable key and is
	// never packed, so writing it lands on the backend immediately — if that
	// happened before this flush, a crash in between would leave index/latest
	// pointing at a snapshot whose bytes (or tree) were never actually
	// uploaded. Flushing first, then pointing index/latest at the result,
	// keeps that pointer always valid.
	if err := bm.store.Flush(ctx); err != nil {
		return nil, fmt.Errorf("flush store before updating index/latest: %w", err)
	}
	if err := updateLatest(bm.store, snapRef, newSeq); err != nil {
		return nil, fmt.Errorf("update index/latest: %w", err)
	}

	// Update snapshot catalog (best-effort).
	AppendSnapshotCatalog(bm.store, snapshotToSummary(snapRef, snap))

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
func (bm *BackupManager) loadLatestSeq() (int, error) {
	_, seq, err := resolveLatest(bm.store)
	return seq, err
}

// findPreviousSnapshot lists all snapshots and returns the most recent one
// whose Source matches the given info. Matching prefers the new identity
// fields and falls back to legacy fields for backward compatibility.
// Returns nil when no matching snapshot exists in an otherwise readable
// catalog; returns an error when the catalog could not be read, since that is
// not the same thing as "no previous snapshot" — treating it as such would
// silently downgrade an incremental backup to a full rescan and reset the
// sequence number.
func (bm *BackupManager) findPreviousSnapshot(info core.SourceInfo) (*core.Snapshot, error) {
	entries, err := LoadSnapshotCatalog(bm.store)
	if err != nil {
		return nil, err
	}

	// Pass 1: identity + path_id (preferred).
	if info.Identity != "" && info.PathID != "" {
		for _, e := range entries {
			if e.Snap.Source != nil &&
				e.Snap.Source.Type == info.Type &&
				e.Snap.Source.Identity == info.Identity &&
				e.Snap.Source.PathID == info.PathID {
				snap := e.Snap
				return &snap, nil
			}
		}
	}

	// Pass 2: identity + path bridge for snapshots without path_id.
	if info.Identity != "" {
		for _, e := range entries {
			if e.Snap.Source != nil &&
				e.Snap.Source.Type == info.Type &&
				e.Snap.Source.Identity == info.Identity &&
				e.Snap.Source.Path == info.Path {
				snap := e.Snap
				return &snap, nil
			}
		}
	}

	// Pass 3: legacy match (type + account + path)
	for _, e := range entries {
		if e.Snap.Source != nil &&
			e.Snap.Source.Type == info.Type &&
			e.Snap.Source.Account == info.Account &&
			e.Snap.Source.Path == info.Path {
			snap := e.Snap
			return &snap, nil
		}
	}
	return nil, nil
}

// writeSnapshotObject builds and writes the snapshot object for the given
// root and sequence number, but deliberately does not update index/latest —
// that pointer is a mutable, unpacked key that lands durably the instant it
// is written, so the caller must flush the store first to make sure the
// snapshot object (and the HAMT nodes it names) are durable before anything
// points at them.
func (bm *BackupManager) writeSnapshotObject(ctx context.Context, root string, seq int, changeToken string) (ref, hash string, snap core.Snapshot, err error) {
	meta := make(map[string]string, len(bm.cfg.meta)+1)
	for k, v := range bm.cfg.meta {
		meta[k] = v
	}
	meta["generator"] = bm.cfg.generator

	snap = core.Snapshot{
		Version:     1,
		Created:     time.Now().Format(time.RFC3339),
		Root:        root,
		Seq:         seq,
		Source:      &bm.sourceInfo,
		Tags:        bm.cfg.tags,
		Meta:        meta,
		ChangeToken: changeToken,
		ExcludeHash: bm.cfg.excludeHash,
	}

	hash, snapData, err := core.ComputeJSONHash(&snap)
	if err != nil {
		return "", "", snap, err
	}

	ref = "snapshot/" + hash
	if err := bm.store.Put(ctx, ref, snapData); err != nil {
		return "", "", snap, err
	}

	return ref, hash, snap, nil
}

func (bm *BackupManager) trackFileMeta(ref string, fm core.FileMeta) {
	bm.newMetas[ref] = fm
}

func (bm *BackupManager) loadMeta(ctx context.Context, ref string) (*core.FileMeta, error) {
	bm.metaCacheMu.RLock()
	fm, ok := bm.metaCache[ref]
	bm.metaCacheMu.RUnlock()
	if ok {
		return &fm, nil
	}

	data, err := getVerified(ctx, bm.store, ref)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil, err
	}
	bm.metaCacheMu.Lock()
	bm.metaCache[ref] = fm
	bm.metaCacheMu.Unlock()
	return &fm, nil
}
