package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/source"
)

var defaultBackupLog = logger.New("backup", logger.ColorGreen)

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

// WithTags adds tags to the backup snapshot.
func WithTags(tags ...string) BackupOption {
	return func(cfg *backupConfig) { cfg.tags = append(cfg.tags, tags...) }
}

// WithGenerator overrides the default generator name in snapshot metadata.
func WithGenerator(name string) BackupOption {
	return func(cfg *backupConfig) { cfg.generator = name }
}

// WithMeta adds a key-value pair to the snapshot metadata.
//
// Keys under core.ReservedMetaPrefix belong to Cloudstic and are rejected when
// the backup runs. The option itself cannot report that, being a plain
// mutator, which is why validateMeta runs first thing in Run.
func WithMeta(key, value string) BackupOption {
	return func(cfg *backupConfig) { cfg.meta[key] = value }
}

// validateMeta rejects user metadata that would impersonate metadata Cloudstic
// writes about a snapshot.
//
// The namespace has to be defended at the point of entry rather than filtered
// on the way out: `copy` decides whether it has already copied a snapshot by
// reading these keys, so a snapshot carrying forged provenance is one that
// `copy` will skip having never copied it — a silent hole in the destination.
func (cfg backupConfig) validateMeta() error {
	keys := make([]string, 0, len(cfg.meta))
	for key := range cfg.meta {
		if core.IsReservedMetaKey(key) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return fmt.Errorf(
		"snapshot metadata key %s is reserved: keys beginning with %q are written by Cloudstic itself",
		strings.Join(keys, ", "), core.ReservedMetaPrefix,
	)
}

// WithExcludeHash records the hash of the active exclude patterns. When this
// differs from the previous snapshot the engine forces a full rescan.
func WithExcludeHash(hash string) BackupOption {
	return func(cfg *backupConfig) { cfg.excludeHash = hash }
}

// BackupManager orchestrates a backup: scanning a source for changes, uploading
// new or modified files, and persisting a snapshot backed by a Merkle-HAMT.
type BackupManager struct {
	// log is this manager's debug sink; the snapshot catalog carries its own.
	log     *logger.Logger
	catalog snapshotCatalog
	source  source.Source
	// store is the key-cached view of the destination, and the only store this
	// manager writes through. It is held at its concrete type so PreloadKeys
	// stays reachable; every other use goes through store.ObjectStore.
	store   *storelayer.KeyCacheStore
	tree    *hamt.Tree
	txn     *hamt.Txn // working tree; opened by scanSource, written by Commit
	chunker *Chunker
	// blobs packs file bodies into blob/ objects. Nil outside format v3,
	// where bodies are content objects and there is nothing to pack.
	blobs *blobWriter
	// bodies reads them back, which a backup does only to move a body out of
	// a blob it is retiring. Nil outside format v3.
	bodies *blobReader
	// consolidation accumulates what the snapshot being written still needs
	// from each blob it inherited, and decides which of those blobs are worth
	// rewriting forward. Built by scan and nowhere else, so it is nil for
	// anything but a full-scan format-v3 backup that is actually writing —
	// see there for why an incremental scan must not feed one.
	consolidation *blobConsolidator
	// reuse maps a content hash to a body placement the repository already
	// holds, so a file whose bytes are already stored is pointed at rather
	// than packed again. Populated by change detection and by this run's own
	// blob writer, bounded in bytes, and released when the upload ends — see
	// bodyindex.go. Nil for anything but a format-v3 backup that is writing.
	reuse      *bodyIndex
	reporter   ui.Reporter
	stats      *backupStats
	sourceInfo core.SourceInfo
	cfg        backupConfig

	// newMetas holds filemetas produced by the current scan, whose bytes are
	// still queued in pendingMetas and so cannot be read back through metas.
	// It is scan-phase only: Run releases it once scanSource returns, so
	// nothing after that point may read or write it.
	newMetas     map[string]core.FileMeta
	metas        *metaLoader
	pendingMetas map[string][]byte // deferred filemeta PUTs (ref → JSON)

	// parentIndex maps fileID → primary parent fileID, so lookupMetaByFileID can
	// build an AffinityKey instead of walking the tree. scanIncremental allocates
	// it; scan leaves it nil.
	//
	// Its only reader is reached from processEntry's len(meta.Paths) == 0 branch.
	// Every source in this module populates Paths in Walk, so a full scan was
	// writing two strings per scanned entry into a map it would then never read;
	// a change feed is what can deliver an entry with no path at all.
	//
	// The gate does not rest on that staying true, because it is a fast path
	// rather than a requirement: a nil map reads as empty and a miss falls
	// through to LookupByKey, so an entry that does arrive pathless under a full
	// scan still resolves, just by walking the tree. MockSource emits exactly
	// such entries, which is how the engine tests cover that fallback.
	parentIndex map[string]string
	hmacKey     []byte

	// v3 is the repository's recorded format (Deps.FormatV3): entries carry
	// their metadata and small content in the leaf, and no filemeta/ or
	// content/ objects are written.
	v3 bool
}

func NewBackupManager(d Deps, src source.Source, opts ...BackupOption) *BackupManager {
	cfg := backupConfig{
		generator: "cloudstic-cli",
		meta:      map[string]string{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	sourceInfo := src.Info()
	keyCache := storelayer.NewKeyCacheStore(d.Store)
	bm := &BackupManager{
		log:          defaultBackupLog.To(d.LogSink),
		catalog:      newSnapshotCatalog(keyCache, d.LogSink),
		source:       src,
		store:        keyCache,
		tree:         hamt.NewTree(keyCache, d.treeOptions(hamt.WithLogger(d.LogSink))...),
		chunker:      NewChunker(keyCache, d.HMACKey),
		reporter:     d.Reporter,
		sourceInfo:   sourceInfo,
		cfg:          cfg,
		newMetas:     make(map[string]core.FileMeta),
		metas:        newMetaLoader(keyCache),
		pendingMetas: make(map[string][]byte),
		hmacKey:      d.HMACKey,
		v3:           d.FormatV3,
	}
	if d.FormatV3 {
		// Bodies go to the store below compression and encryption, since a
		// blob's members carry their own of each (RFC 0026). Not through
		// keyCache: a blob is named by its member sequence, so a repeat is
		// possible but rare, and caching one would hold megabytes to save a
		// request that is almost never made.
		bm.blobs = newBlobWriter(d.BlobStore, d.Sealer)
		bm.bodies = d.blobReader()
	}
	return bm
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
	if bm.v3 && bm.blobs == nil {
		return nil, errors.New("backup: format v3 needs a blob store; none was configured")
	}
	res, err := bm.run(ctx)
	if err != nil {
		return nil, err
	}
	if !bm.cfg.dryRun {
		bm.compactPackIndex(ctx)
	}
	return res, nil
}

// compactPackIndex consolidates the pack index when it has grown past
// packIndexCompactThreshold, and only if it can do so without getting in
// anyone's way.
//
// It runs after run returned, which is after the shared lock was released, and
// takes the exclusive lock rather than compacting under the shared one.
// Compaction deletes the shards this store absorbed, and a concurrent reader
// that listed them before the delete and reads them after fails — a spurious
// error rather than data loss, but a backup tidying up on its way out must not
// cause it. AcquireRepoLock fails immediately when any lock is held, so
// concurrent backups skip this and whichever finishes alone does the work.
//
// Every failure is swallowed by design. The backup itself has already
// succeeded; all that is lost is a consolidation the next backup will reach
// again, and turning "someone else holds the lock" into a backup failure would
// be strictly worse than reading a few more index objects.
func (bm *BackupManager) compactPackIndex(ctx context.Context) {
	// A v3 repository has no packfile layer and therefore no index to
	// consolidate. Asked rather than discovered through findPackStore's walk,
	// so the format decides rather than the chain's shape happening to agree.
	if bm.v3 {
		return
	}
	ps := findPackStore(bm.store)
	if ps == nil || ps.IndexObjectCount() <= packIndexCompactThreshold {
		return
	}

	lock, lockedCtx, err := AcquireRepoLock(ctx, bm.store, "compact pack index")
	if err != nil {
		bm.log.Debugf("skipping pack index compaction: %v", err)
		return
	}
	defer lock.Release()

	removed, err := ps.CompactCatalog(lockedCtx)
	if err != nil {
		bm.log.Debugf("pack index compaction failed: %v", err)
		return
	}
	bm.log.Debugf("consolidated %d pack index objects", removed)
}

func (bm *BackupManager) run(ctx context.Context) (*RunResult, error) {
	if err := bm.cfg.validateMeta(); err != nil {
		return nil, err
	}

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
			bm.log.Debugf("exclude patterns changed (old=%q new=%q), forcing full rescan", oldHash, newHash)
			changeToken = ""
		} else if newHash != "" {
			bm.log.Debugf("exclude patterns unchanged (hash=%q), continuing incremental", newHash)
		}
	} else if prevSnap == nil {
		bm.log.Debugf("no previous snapshot found, running full scan")
	}

	// Built before the scan rather than inside it, because both scan
	// strategies feed it: a change feed resolves the previous entry of every
	// upsert it reports, which is where an inherited placement comes from.
	// A dry run writes nothing and uploads nothing, so it has no use for one.
	if bm.v3 && !bm.cfg.dryRun && bm.blobs != nil {
		bm.reuse = newBodyIndex()
	}

	pending, totalBytes, newToken, usedFullScan, err := bm.scanSource(ctx, oldRoot, changeToken)
	if err != nil {
		return nil, err
	}

	// newMetas exists to answer parent lookups for entries whose filemeta has
	// not been written yet, which only happens while scanning. Its last reader
	// returned with scanSource, so it is released here rather than carried
	// through upload — the longest and most allocation-heavy phase, where it
	// would hold a core.FileMeta per scanned file for nothing.
	//
	// Releasing by nilling is deliberate: a nil map still reads as empty, but a
	// write to one panics, so a lookup reintroduced below this line fails
	// loudly instead of quietly repopulating a map nothing consumes.
	bm.newMetas = nil

	// Same reasoning, one phase wider. The scan reads the previous filemeta of
	// every entry it visits — that is what change detection is — and memoizes
	// them because it revisits parents. Nothing below reads them again except
	// countRemoved, and only for entries that are gone, so keeping a FileMeta
	// per scanned file through the upload buys a handful of hits and holds
	// memory proportional to the source.
	bm.metas.releaseCache()

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
	// content/ is a v2 namespace: in v3 a small file's bytes live in its leaf
	// and a chunked file's manifest with it, so listing it would cost a
	// request per backup to enumerate nothing.
	preload := []string{"chunk/", "content/", "node/"}
	if bm.v3 {
		preload = []string{"chunk/", "node/"}
	}
	if err := bm.store.PreloadKeys(ctx, preload...); err != nil {
		return nil, fmt.Errorf("preload key cache: %w", err)
	}

	if err := bm.upload(ctx, pending, totalBytes); err != nil {
		return nil, err
	}

	// The index belongs to the upload, which is over: every worker has
	// reported, so nothing reads it after this point. Released here rather
	// than left to the manager's lifetime for two reasons — it is the one
	// structure here sized by the repository's distinct bodies, and
	// consolidation below must not see it. Consolidation moves a body *out*
	// of the blob it is retiring, and the placement the index would hand back
	// is the one inside that blob; it calls the writer directly, and this
	// makes that independent of the order these phases happen to run in.
	if hits, saved, entries := bm.reuse.stats(); hits > 0 {
		bm.log.Debugf("whole-file dedup: %d bodies reused, %d bytes not stored, %d placements held",
			hits, saved, entries)
	}
	bm.reuse = nil

	if usedFullScan {
		if err := bm.countRemoved(ctx, oldRoot); err != nil {
			return nil, fmt.Errorf("counting removed entries: %w", err)
		}
	}

	// Consolidation runs after the upload rather than during it, so a body it
	// moves joins the blob the writer opens next instead of displacing the
	// content this backup came here to write; after countRemoved, whose diff
	// counts what the *source* dropped and has no opinion about where a body
	// lives; and before the commit, so the entries it repoints are part of the
	// snapshot about to be written.
	if bm.shouldConsolidate() {
		if err := bm.consolidate(ctx); err != nil {
			return nil, fmt.Errorf("consolidate blobs: %w", err)
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
	bm.catalog.add(snapshotToSummary(snapRef, snap))

	r := bm.buildResult()
	r.SnapshotHash = snapHash
	r.SnapshotRef = snapRef
	r.Root = newRoot
	return r, nil
}

// shouldConsolidate reports whether this run may rewrite bodies forward.
//
// The one case a full-scan v3 backup still declines is a caller that asked for
// an unchanged tree to produce no snapshot. Consolidation changes the tree —
// that is what it is for — so running it would turn every such backup into a
// written snapshot, which is precisely the outcome WithIgnoreEmptySnapshot
// exists to prevent. A run that changed nothing has also introduced no new
// blob, so there is nothing new to consolidate away.
func (bm *BackupManager) shouldConsolidate() bool {
	if bm.consolidation == nil {
		return false
	}
	if !bm.cfg.ignoreEmptySnapshot {
		return true
	}
	changed := bm.stats.filesNew.Load() + bm.stats.filesChanged.Load() + bm.stats.filesRemoved.Load() +
		bm.stats.dirsNew.Load() + bm.stats.dirsChanged.Load() + bm.stats.dirsRemoved.Load()
	return changed > 0
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
	entries, err := bm.catalog.load()
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
