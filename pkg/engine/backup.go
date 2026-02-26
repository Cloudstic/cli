package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/hamt"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/ui"
)

const uploadConcurrency = 10

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
	bytesAddedRaw   atomic.Int64
	startTime       time.Time
}

// BackupOption configures a backup operation.
type BackupOption func(*backupConfig)

type backupConfig struct {
	verbose   bool
	tags      []string
	generator string
	meta      map[string]string
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
	store      *store.MeteredStore
	tree       *hamt.Tree
	cache      *hamt.TransactionalStore
	chunker    *Chunker
	reporter   ui.Reporter
	stats      *backupStats
	sourceInfo core.SourceInfo
	cfg        backupConfig
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
	meteredStore := store.NewMeteredStore(dest)
	cache := hamt.NewTransactionalStore(meteredStore)
	return &BackupManager{
		source:     src,
		store:      meteredStore,
		tree:       hamt.NewTree(cache),
		cache:      cache,
		chunker:    NewChunker(meteredStore),
		reporter:   reporter,
		sourceInfo: sourceInfo,
		cfg:        cfg,
	}
}

// Run executes a full backup: scan the source for changes, upload new/modified
// files, build a new HAMT root, and persist a snapshot.
// RunResult holds the outcome of a successful backup run.
type RunResult struct {
	SnapshotHash    string
	SnapshotRef     string
	Root            string
	FilesNew        int64
	FilesChanged    int64
	FilesUnmodified int64
	FilesRemoved    int64
	DirsNew         int64
	DirsChanged     int64
	DirsUnmodified  int64
	DirsRemoved     int64
	BytesAddedRaw   int64
	BytesAdded      int64
	Duration        time.Duration
}

func (bm *BackupManager) Run(ctx context.Context) (*RunResult, error) {
	seq := bm.loadLatestSeq()
	prevSnap := bm.findPreviousSnapshot(bm.sourceInfo)
	bm.stats = &backupStats{startTime: time.Now()}

	var oldRoot string
	var changeToken string
	if prevSnap != nil {
		oldRoot = prevSnap.Root
		changeToken = prevSnap.ChangeToken
	}

	var newRoot string
	var newToken string
	var pending []core.FileMeta
	var totalBytes int64
	var err error
	usedFullScan := false

	if incSrc, ok := bm.source.(store.IncrementalSource); ok {
		if changeToken == "" {
			// First run (or previous snapshot deleted): capture the token
			// BEFORE the full walk so changes during the walk are not lost.
			newToken, err = incSrc.GetStartPageToken()
			if err != nil {
				return nil, fmt.Errorf("get start page token: %w", err)
			}
			newRoot, pending, totalBytes, err = bm.scan(ctx, oldRoot)
			if err != nil {
				return nil, err
			}
			usedFullScan = true
		} else {
			newRoot, pending, totalBytes, newToken, err = bm.scanIncremental(ctx, oldRoot, incSrc, changeToken)
			if err != nil {
				return nil, err
			}
		}
	} else {
		newRoot, pending, totalBytes, err = bm.scan(ctx, oldRoot)
		if err != nil {
			return nil, err
		}
		usedFullScan = true
	}

	newRoot, err = bm.upload(pending, totalBytes, newRoot)
	if err != nil {
		return nil, err
	}

	if usedFullScan {
		if err := bm.countRemoved(oldRoot, newRoot); err != nil {
			return nil, fmt.Errorf("counting removed entries: %w", err)
		}
	}

	snapRef, snapHash, err := bm.saveSnapshot(newRoot, seq+1, newToken)
	if err != nil {
		return nil, err
	}

	if err := bm.cache.Flush(newRoot); err != nil {
		return nil, fmt.Errorf("flush hamt nodes: %w", err)
	}
	bytesAdded := bm.store.BytesWritten()
	bm.store.Reset()
	return &RunResult{
		SnapshotHash:    snapHash,
		SnapshotRef:     snapRef,
		Root:            newRoot,
		FilesNew:        bm.stats.filesNew.Load(),
		FilesChanged:    bm.stats.filesChanged.Load(),
		FilesUnmodified: bm.stats.filesUnmodified.Load(),
		FilesRemoved:    bm.stats.filesRemoved.Load(),
		DirsNew:         bm.stats.dirsNew.Load(),
		DirsChanged:     bm.stats.dirsChanged.Load(),
		DirsUnmodified:  bm.stats.dirsUnmodified.Load(),
		DirsRemoved:     bm.stats.dirsRemoved.Load(),
		BytesAddedRaw:   bm.stats.bytesAddedRaw.Load(),
		BytesAdded:      bytesAdded,
		Duration:        time.Since(bm.stats.startTime),
	}, nil
}

// ---------------------------------------------------------------------------
// Phase 0: load previous state
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Phase 1: scan
// ---------------------------------------------------------------------------

// scan walks the source and builds a new HAMT root containing unmodified and
// folder entries. New or changed files are returned in pending for upload.
func (bm *BackupManager) scan(ctx context.Context, oldRoot string) (newRoot string, pending []core.FileMeta, totalBytes int64, err error) {
	phase := bm.reporter.StartPhase("Scanning", 0, false)

	err = bm.source.Walk(ctx, func(meta core.FileMeta) error {
		phase.Increment(1)

		changed, oldRef, cerr := bm.detectChange(oldRoot, &meta)
		if cerr != nil {
			return cerr
		}

		if !changed {
			bm.recordStat(meta.Type, false, false)
			newRoot, err = bm.tree.Insert(newRoot, meta.FileID, oldRef)
			if err != nil {
				return fmt.Errorf("hamt insert: %w", err)
			}
			return nil
		}

		bm.recordStat(meta.Type, true, oldRef == "")

		if meta.Type == core.FileTypeFolder {
			newRoot, err = bm.insertFolder(newRoot, &meta, phase)
			return err
		}

		if bm.cfg.verbose {
			phase.Log(fmt.Sprintf("Queueing: %s", meta.Name))
		}
		pending = append(pending, meta)
		totalBytes += meta.Size
		return nil
	})

	if err != nil {
		phase.Error()
		return "", nil, 0, err
	}
	phase.Done()
	return newRoot, pending, totalBytes, nil
}

// detectChange compares meta against the previous snapshot. It returns whether
// the entry changed, and the old value ref (empty when the entry is new).
//
// For sources that do not provide a content hash (e.g. Google Drive), a
// fast-path compares observable metadata and carries the hash forward to avoid
// false-positive diffs.
func (bm *BackupManager) detectChange(oldRoot string, meta *core.FileMeta) (changed bool, oldRef string, err error) {
	oldRef, err = bm.tree.Lookup(oldRoot, meta.FileID)
	if err != nil {
		return false, "", fmt.Errorf("hamt lookup: %w", err)
	}
	if oldRef == "" {
		return true, "", nil
	}

	oldMeta, err := bm.loadMeta(oldRef)
	if err != nil {
		return false, "", err
	}

	if meta.ContentHash == "" && oldMeta.ContentHash != "" && metadataEqual(*meta, *oldMeta) {
		meta.ContentHash = oldMeta.ContentHash
	}

	newRef, _, err := meta.Ref()
	if err != nil {
		return false, "", err
	}
	return newRef != oldRef, oldRef, nil
}

func metadataEqual(a, b core.FileMeta) bool {
	return a.Name == b.Name &&
		a.Size == b.Size &&
		a.Mtime == b.Mtime &&
		a.Type == b.Type &&
		len(a.Parents) == len(b.Parents)
}

func (bm *BackupManager) insertFolder(root string, meta *core.FileMeta, phase ui.Phase) (string, error) {
	if bm.cfg.verbose {
		phase.Log(fmt.Sprintf("Folder: %s (New/Changed)", meta.Name))
	}
	meta.ContentHash = ""
	meta.Size = 0

	metaRef, metaData, err := meta.Ref()
	if err != nil {
		return "", err
	}
	if err := bm.store.Put(metaRef, metaData); err != nil {
		return "", err
	}
	return bm.tree.Insert(root, meta.FileID, metaRef)
}

// recordStat increments the appropriate counter in bm.stats.
func (bm *BackupManager) recordStat(ft core.FileType, changed, isNew bool) {
	switch {
	case !changed && ft == core.FileTypeFolder:
		bm.stats.dirsUnmodified.Add(1)
	case !changed:
		bm.stats.filesUnmodified.Add(1)
	case isNew && ft == core.FileTypeFolder:
		bm.stats.dirsNew.Add(1)
	case isNew:
		bm.stats.filesNew.Add(1)
	case ft == core.FileTypeFolder:
		bm.stats.dirsChanged.Add(1)
	default:
		bm.stats.filesChanged.Add(1)
	}
}

func (bm *BackupManager) recordRemoved(ft core.FileType) {
	if ft == core.FileTypeFolder {
		bm.stats.dirsRemoved.Add(1)
	} else {
		bm.stats.filesRemoved.Add(1)
	}
}

// countRemoved uses a structural HAMT diff to count entries present in oldRoot
// but absent from newRoot (full-scan path where deletions are implicit).
func (bm *BackupManager) countRemoved(oldRoot, newRoot string) error {
	if oldRoot == "" {
		return nil
	}
	return bm.tree.Diff(oldRoot, newRoot, func(d hamt.DiffEntry) error {
		if d.OldValue != "" && d.NewValue == "" {
			meta, err := bm.loadMeta(d.OldValue)
			if err != nil {
				return err
			}
			bm.recordRemoved(meta.Type)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Phase 1b: incremental scan (IncrementalSource)
// ---------------------------------------------------------------------------

// scanIncremental applies deltas from WalkChanges on top of the previous HAMT
// root, producing a new root and a list of files pending upload.
func (bm *BackupManager) scanIncremental(ctx context.Context, oldRoot string, incSrc store.IncrementalSource, token string) (newRoot string, pending []core.FileMeta, totalBytes int64, newToken string, err error) {
	phase := bm.reporter.StartPhase("Scanning (incremental)", 0, false)
	newRoot = oldRoot

	newToken, walkErr := incSrc.WalkChanges(ctx, token, func(fc store.FileChange) error {
		phase.Increment(1)

		switch fc.Type {
		case store.ChangeDelete:
			bm.recordRemoved(fc.Meta.Type)
			newRoot, err = bm.tree.Delete(newRoot, fc.Meta.FileID)
			if err != nil {
				return fmt.Errorf("hamt delete %s: %w", fc.Meta.FileID, err)
			}

		case store.ChangeUpsert:
			changed, oldRef, cerr := bm.detectChange(oldRoot, &fc.Meta)
			if cerr != nil {
				return cerr
			}

			if !changed {
				bm.recordStat(fc.Meta.Type, false, false)
				newRoot, err = bm.tree.Insert(newRoot, fc.Meta.FileID, oldRef)
				if err != nil {
					return fmt.Errorf("hamt insert: %w", err)
				}
				return nil
			}

			bm.recordStat(fc.Meta.Type, true, oldRef == "")

			if fc.Meta.Type == core.FileTypeFolder {
				newRoot, err = bm.insertFolder(newRoot, &fc.Meta, phase)
				return err
			}

			if bm.cfg.verbose {
				phase.Log(fmt.Sprintf("Queueing: %s", fc.Meta.Name))
			}
			pending = append(pending, fc.Meta)
			totalBytes += fc.Meta.Size
		}
		return nil
	})

	if walkErr != nil {
		phase.Error()
		return "", nil, 0, "", walkErr
	}

	phase.Done()
	return newRoot, pending, totalBytes, newToken, nil
}

// ---------------------------------------------------------------------------
// Phase 2: upload
// ---------------------------------------------------------------------------

type uploadResult struct {
	fileID             string
	ref                string
	size               int64
	newBytes           int64
	newBytesCompressed int64
	err                error
}

// upload processes the pending file queue with concurrent workers, inserts each
// result into the HAMT, and returns the updated root.
func (bm *BackupManager) upload(pending []core.FileMeta, totalBytes int64, root string) (string, error) {
	if len(pending) == 0 {
		return root, nil
	}

	phase := bm.reporter.StartPhase("Uploading", totalBytes, true)

	jobs := make(chan core.FileMeta, len(pending))
	results := make(chan uploadResult, len(pending))

	for range min(uploadConcurrency, len(pending)) {
		go func() {
			for meta := range jobs {
				results <- bm.processFile(meta, phase)
			}
		}()
	}

	for _, m := range pending {
		jobs <- m
	}
	close(jobs)

	var err error
	for range pending {
		res := <-results
		if res.err != nil {
			phase.Error()
			return "", res.err
		}
		bm.stats.bytesAddedRaw.Add(res.newBytes)
		root, err = bm.tree.Insert(root, res.fileID, res.ref)
		if err != nil {
			phase.Error()
			return "", fmt.Errorf("hamt insert: %w", err)
		}
	}

	phase.Done()
	return root, nil
}

// processFile uploads (or deduplicates) a single file's content and persists
// its FileMeta. It is safe to call from multiple goroutines.
func (bm *BackupManager) processFile(meta core.FileMeta, phase ui.Phase) uploadResult {
	if bm.cfg.verbose {
		phase.Log(fmt.Sprintf("Processing: %s", meta.Name))
	}

	contentHash, size, newBytes, newBytesCompressed, err := bm.uploadContent(meta, phase)
	if err != nil {
		return uploadResult{err: err}
	}

	meta.ContentHash = contentHash
	meta.Size = size

	metaRef, metaData, err := meta.Ref()
	if err != nil {
		return uploadResult{err: err}
	}
	if err := bm.store.Put(metaRef, metaData); err != nil {
		return uploadResult{err: err}
	}
	return uploadResult{fileID: meta.FileID, ref: metaRef, size: size, newBytes: newBytes, newBytesCompressed: newBytesCompressed}
}

// uploadContent streams, chunks, and stores the file content. If the content
// already exists (dedup by hash), the upload is skipped and newBytes is zero.
func (bm *BackupManager) uploadContent(meta core.FileMeta, phase ui.Phase) (hash string, size, newBytes, newBytesCompressed int64, err error) {
	if meta.ContentHash != "" {
		exists, err := bm.store.Exists("content/" + meta.ContentHash)
		if err == nil && exists {
			if bm.cfg.verbose {
				phase.Log(fmt.Sprintf("Deduplicated: %s", meta.Name))
			}
			return meta.ContentHash, meta.Size, 0, 0, nil
		}
	}

	rc, err := bm.source.GetFileStream(meta.FileID)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("get stream for %s: %w", meta.FileID, err)
	}
	defer rc.Close()

	chunkRefs, size, newBytes, newBytesCompressed, hash, err := bm.chunker.ProcessStream(rc, func(n int64) {
		phase.Increment(n)
	})
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("chunking %s: %w", meta.Name, err)
	}

	if _, err := bm.chunker.CreateContentObject(chunkRefs, size, hash); err != nil {
		return "", 0, 0, 0, fmt.Errorf("create content for %s: %w", meta.Name, err)
	}
	return hash, size, newBytes, newBytesCompressed, nil
}

// ---------------------------------------------------------------------------
// Phase 3: persist snapshot
// ---------------------------------------------------------------------------

func (bm *BackupManager) saveSnapshot(root string, seq int, changeToken string) (ref, hash string, err error) {
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
	if err := bm.store.Put(ref, snapData); err != nil {
		return "", "", err
	}

	if err := updateLatest(bm.store, ref, seq); err != nil {
		return "", "", err
	}

	_ = AddSnapshotToIndex(bm.store, snap, ref)

	return ref, hash, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (bm *BackupManager) loadMeta(ref string) (*core.FileMeta, error) {
	data, err := bm.store.Get(ref)
	if err != nil {
		return nil, err
	}
	var fm core.FileMeta
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil, err
	}
	return &fm, nil
}
