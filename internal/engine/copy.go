package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

var defaultCopyLog = logger.New("copy", logger.ColorCyan)

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

// CopyOption configures a copy run.
type CopyOption func(*copyConfig)

type copyConfig struct {
	snapshotIDs []string
	filter      snapshotFilter
	since       time.Time
	hasSince    bool
	dryRun      bool
	allowCopied bool
}

// WithCopySnapshotIDs restricts the copy to the named source snapshots. The
// filters still apply on top, narrowing this set further.
func WithCopySnapshotIDs(ids ...string) CopyOption {
	return func(cfg *copyConfig) { cfg.snapshotIDs = append(cfg.snapshotIDs, ids...) }
}

// WithCopyFilterSource restricts the copy to snapshots of the given source type.
func WithCopyFilterSource(source string) CopyOption {
	return func(cfg *copyConfig) { cfg.filter.source = source }
}

// WithCopyFilterPath restricts the copy to snapshots of the given source path.
func WithCopyFilterPath(path string) CopyOption {
	return func(cfg *copyConfig) { cfg.filter.path = path }
}

// WithCopyFilterAccount restricts the copy to snapshots of the given account.
func WithCopyFilterAccount(account string) CopyOption {
	return func(cfg *copyConfig) { cfg.filter.account = account }
}

// WithCopyFilterTag restricts the copy to snapshots carrying the given tag.
// Repeating it requires all the named tags.
func WithCopyFilterTag(tag string) CopyOption {
	return func(cfg *copyConfig) { cfg.filter.tags = append(cfg.filter.tags, tag) }
}

// WithCopySince restricts the copy to snapshots created at or after t.
//
// This is the composable answer to destination retention: a scheduled copy
// passes its previous run's start time, so snapshots the destination has since
// forgotten are not resurrected on the next run (RFC 0017 §7).
func WithCopySince(t time.Time) CopyOption {
	return func(cfg *copyConfig) { cfg.since, cfg.hasSince = t, true }
}

// WithCopyDryRun resolves and reports the selection without writing anything.
func WithCopyDryRun() CopyOption {
	return func(cfg *copyConfig) { cfg.dryRun = true }
}

// WithCopyAllowCopied permits copying snapshots that already carry provenance
// from an earlier copy, re-stamping them with the immediate source.
func WithCopyAllowCopied() CopyOption {
	return func(cfg *copyConfig) { cfg.allowCopied = true }
}

// ---------------------------------------------------------------------------
// Results
// ---------------------------------------------------------------------------

// CopiedSnapshot records one snapshot transferred into the destination.
type CopiedSnapshot struct {
	SourceRef string           `json:"source_ref"`
	DestRef   string           `json:"dest_ref"`
	Created   string           `json:"created"`
	Source    *core.SourceInfo `json:"source,omitempty"`
}

// SkippedSnapshot records one snapshot the destination already had.
type SkippedSnapshot struct {
	SourceRef string           `json:"source_ref"`
	DestRef   string           `json:"dest_ref"`
	Created   string           `json:"created"`
	Reason    string           `json:"reason"`
	Source    *core.SourceInfo `json:"source,omitempty"`
}

// CopyResult reports what a copy run did.
type CopyResult struct {
	Copied       []CopiedSnapshot  `json:"copied"`
	Skipped      []SkippedSnapshot `json:"skipped"`
	BytesRead    int64             `json:"bytes_read"`
	BytesWritten int64             `json:"bytes_written"`
	DryRun       bool              `json:"dry_run"`
	SourceRepoID string            `json:"source_repo_id,omitempty"`
	DestRepoID   string            `json:"dest_repo_id,omitempty"`
	Duration     time.Duration     `json:"duration"`
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// CopySide describes the repository a copy reads from.
//
// RepoID may be empty: a repository written before RepoConfig.ID existed has
// none, and one whose marker was rewritten by an older build has lost it. That
// is handled by CopyProvenance.Matches rather than treated as an error.
type CopySide struct {
	Store  store.ObjectStore
	RepoID string
}

// CopyManager transfers snapshots from one repository into another.
//
// The transfer is a rebuild, not a copy of bytes. Every object reference in a
// repository is derived from that repository's master key — chunk and content
// refs through an HMAC of the plaintext, filemeta and node refs through hashes
// that embed them — so an object written to the destination necessarily has a
// different name than it had in the source. The graph is therefore reassembled
// bottom-up, remapping each level's references as it goes (RFC 0017 §4).
type CopyManager struct {
	src     CopySide
	dst     store.ObjectStore
	dstHMAC []byte
	dstID   string

	reporter ui.Reporter
	log      *logger.Logger
	snapLog  *logger.Logger

	srcTree *hamt.Tree
	dstTree *hamt.Tree

	// The remap tables live for the whole run rather than per snapshot.
	//
	// Consecutive snapshots of one source share the overwhelming majority of
	// their graph, so a per-snapshot table re-reads every file that appears in
	// more than one snapshot. Measured on a 2000-file tree at eight snapshots,
	// clearing the tables between snapshots took plaintext read from 2.2 MB to
	// 7.1 MB and wall time from 89 ms to 198 ms.
	//
	// It is not the only thing keeping that bounded, which is worth knowing
	// before trusting a benchmark: copyContent checks the destination before
	// reading anything, so bulk data is not re-read even with no table at all.
	// What the tables save is the metadata read and the destination round trip
	// per file per snapshot.
	chunkRefs   map[string]string
	contentRefs map[string]string
	metaRefs    map[string]copiedMeta

	// lastCopied remembers, per lineage, the source tree most recently copied
	// in this run and the destination tree it produced. Holding both is what
	// lets the next snapshot of that lineage be applied as a diff: the pair is
	// a proven translation, so changes between two source roots map exactly
	// onto changes against that destination root.
	lastCopied map[string]copiedTree

	// bytesRead accumulates plaintext read from the source. The copy loop is
	// sequential, so this needs no synchronisation.
	bytesRead int64
}

// copiedMeta is what the remap table remembers about a filemeta already
// rebuilt in the destination.
//
// The affinity key is stored alongside the ref because reconstructing it needs
// the file's parent, which is only in the metadata object. Keeping it here is
// what lets a cache hit skip the source read entirely — and a hit is the common
// case, since a file that did not change between two snapshots appears in both.
type copiedMeta struct {
	ref         string
	affinityKey string
}

// copiedTree pairs a source tree with the destination tree copy produced from
// it, so the next snapshot of the same lineage can be applied incrementally.
type copiedTree struct {
	sourceRoot string
	destRoot   string
}

// NewCopyManager builds a manager that reads from src and writes to dst.
//
// dstHMAC is the destination's dedup key, and is what every rewritten
// reference is computed under; pass nil for an unencrypted destination.
func NewCopyManager(
	src CopySide,
	dst store.ObjectStore,
	dstHMAC []byte,
	dstRepoID string,
	reporter ui.Reporter,
	logWriter io.Writer,
) *CopyManager {
	return &CopyManager{
		src:         src,
		dst:         dst,
		dstHMAC:     dstHMAC,
		dstID:       dstRepoID,
		reporter:    reporter,
		log:         defaultCopyLog.To(logWriter),
		snapLog:     SnapshotLogger(logWriter),
		srcTree:     hamt.NewTree(src.Store, hamt.WithLogger(logWriter)),
		dstTree:     hamt.NewTree(dst, hamt.WithLogger(logWriter)),
		chunkRefs:   map[string]string{},
		contentRefs: map[string]string{},
		metaRefs:    map[string]copiedMeta{},
		lastCopied:  map[string]copiedTree{},
	}
}

// Run performs the copy.
func (cm *CopyManager) Run(ctx context.Context, opts ...CopyOption) (*CopyResult, error) {
	started := time.Now()
	var cfg copyConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	if cm.src.RepoID != "" && cm.src.RepoID == cm.dstID {
		return nil, fmt.Errorf(
			"source and destination are the same repository (%s): copying a repository into itself would duplicate its history",
			cm.src.RepoID,
		)
	}

	result := &CopyResult{
		DryRun:       cfg.dryRun,
		SourceRepoID: cm.src.RepoID,
		DestRepoID:   cm.dstID,
	}

	sourceEntries, err := LoadSnapshotCatalog(cm.src.Store, cm.snapLog)
	if err != nil {
		return nil, fmt.Errorf("read source snapshot catalog: %w", err)
	}
	selected, err := cm.selectSnapshots(ctx, cfg, sourceEntries)
	if err != nil {
		return nil, err
	}

	// One read of the destination catalog serves both questions asked of it:
	// what has already been copied, and which tree each lineage should seed
	// from.
	destEntries, err := LoadSnapshotCatalog(cm.dst, cm.snapLog)
	if err != nil {
		return nil, fmt.Errorf("read destination snapshot catalog: %w", err)
	}
	alreadyCopied := newProvenanceIndex(destEntries)
	cm.primeLastCopied(destEntries, sourceEntries)

	pending := make([]SnapshotEntry, 0, len(selected))
	for _, entry := range selected {
		prov := core.CopyProvenance{RepoID: cm.src.RepoID, SnapshotRef: entry.Ref}
		if destRef, ok := alreadyCopied.lookup(prov); ok {
			result.Skipped = append(result.Skipped, SkippedSnapshot{
				SourceRef: entry.Ref,
				DestRef:   destRef,
				Created:   entry.Snap.Created,
				Source:    entry.Snap.Source,
				Reason:    "already copied",
			})
			continue
		}
		pending = append(pending, entry)
	}

	if cfg.dryRun {
		// A dry run reports the same selection a real run would act on, with
		// the destination refs left empty because they are only knowable once
		// the objects they name have been written.
		for _, entry := range pending {
			result.Copied = append(result.Copied, CopiedSnapshot{
				SourceRef: entry.Ref,
				Created:   entry.Snap.Created,
				Source:    entry.Snap.Source,
			})
		}
		result.Duration = time.Since(started)
		return result, nil
	}

	if len(pending) == 0 {
		result.Duration = time.Since(started)
		return result, nil
	}

	// Shared locks on both repositories, source first. Shared locks do not
	// exclude one another, so two copies running in opposite directions
	// between the same pair cannot deadlock on the acquisition order. What
	// they do exclude is a concurrent prune: on the source it would collect
	// objects out from under the walk, and on the destination it would collect
	// objects written here but not yet reachable from any snapshot.
	// The source lock is best-effort, and only because taking it is itself a
	// write. Copying from a repository you can only read is a supported and
	// useful configuration, so failing to *place* a lock there must not fail
	// the copy — but finding one already held must, since that means a prune or
	// forget is running and objects are being collected as we walk.
	srcLock, lockedCtx, err := AcquireSharedLock(ctx, cm.src.Store, "copy (source)")
	switch {
	case err == nil:
		defer srcLock.Release()
		ctx = lockedCtx
	case errors.Is(err, ErrRepoLocked):
		return nil, fmt.Errorf("lock source repository: %w", err)
	default:
		cm.log.Debugf("proceeding without a source lock (%v); the source is read-only "+
			"or otherwise refused the lock, so a concurrent prune there could remove "+
			"objects mid-copy", err)
	}

	dstLock, dstCtx, err := AcquireSharedLock(ctx, cm.dst, "copy")
	if err != nil {
		return nil, fmt.Errorf("lock destination repository: %w", err)
	}
	defer dstLock.Release()
	ctx = dstCtx

	defer func() { _ = cm.dst.Flush(ctx) }()

	seq, err := cm.nextSeq()
	if err != nil {
		return nil, err
	}

	for _, entry := range pending {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !cfg.allowCopied {
			if _, copied := core.CopyProvenanceFromMeta(entry.Snap.Meta); copied {
				return nil, fmt.Errorf(
					"snapshot %s was itself produced by copy: pass -allow-copied to re-stamp its provenance to this source",
					shortRef(entry.Ref),
				)
			}
		}

		destRef, err := cm.copySnapshot(ctx, entry, seq)
		if err != nil {
			return nil, fmt.Errorf("copy snapshot %s: %w", shortRef(entry.Ref), err)
		}
		seq++

		result.Copied = append(result.Copied, CopiedSnapshot{
			SourceRef: entry.Ref,
			DestRef:   destRef,
			Created:   entry.Snap.Created,
			Source:    entry.Snap.Source,
		})
	}

	if err := cm.reconcileLatest(ctx, result.Copied); err != nil {
		return nil, err
	}

	result.BytesRead = cm.bytesRead
	result.Duration = time.Since(started)
	return result, nil
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

// selectSnapshots resolves the source snapshots to copy, in the order they
// must be written: ascending Created, ties broken by ascending source Seq.
//
// The ordering is load-bearing. Destination sequence numbers are allocated in
// write order, so copying oldest-first is what keeps the destination's Seq
// ordering agreeing with its Created ordering (RFC 0017 §4.1).
func (cm *CopyManager) selectSnapshots(
	ctx context.Context, cfg copyConfig, entries []SnapshotEntry,
) ([]SnapshotEntry, error) {
	var err error
	if len(cfg.snapshotIDs) > 0 {
		entries, err = cm.resolveExplicit(ctx, entries, cfg.snapshotIDs)
		if err != nil {
			return nil, err
		}
	}

	selected := make([]SnapshotEntry, 0, len(entries))
	for _, entry := range entries {
		if !cfg.filter.IsEmpty() && !matchesFilter(&entry.Snap, cfg.filter) {
			continue
		}
		if cfg.hasSince && entry.Created.Before(cfg.since) {
			continue
		}
		selected = append(selected, entry)
	}

	sort.SliceStable(selected, func(i, j int) bool {
		if !selected[i].Created.Equal(selected[j].Created) {
			return selected[i].Created.Before(selected[j].Created)
		}
		return selected[i].Snap.Seq < selected[j].Snap.Seq
	})
	return selected, nil
}

// resolveExplicit narrows entries to the named snapshots, resolving each
// selector the way every other command does so that "latest" and abbreviated
// hashes mean here what they mean elsewhere.
func (cm *CopyManager) resolveExplicit(
	ctx context.Context, entries []SnapshotEntry, ids []string,
) ([]SnapshotEntry, error) {
	byRef := make(map[string]SnapshotEntry, len(entries))
	for _, entry := range entries {
		byRef[entry.Ref] = entry
	}

	seen := map[string]bool{}
	out := make([]SnapshotEntry, 0, len(ids))
	for _, id := range ids {
		ref, err := resolveSnapshotRef(ctx, cm.src.Store, id)
		if err != nil {
			return nil, err
		}
		if seen[ref] {
			continue
		}
		entry, ok := byRef[ref]
		if !ok {
			return nil, snapshotNotFoundError(id)
		}
		seen[ref] = true
		out = append(out, entry)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Provenance index
// ---------------------------------------------------------------------------

// provenanceIndex answers "has this source snapshot already been copied here".
type provenanceIndex struct {
	entries map[string]string // CopyProvenance.SnapshotRef → destination ref
	repoIDs map[string]string // CopyProvenance.SnapshotRef → recorded source repo id
}

func (p provenanceIndex) lookup(want core.CopyProvenance) (string, bool) {
	destRef, ok := p.entries[want.SnapshotRef]
	if !ok {
		return "", false
	}
	have := core.CopyProvenance{RepoID: p.repoIDs[want.SnapshotRef], SnapshotRef: want.SnapshotRef}
	if !have.Matches(want) {
		return "", false
	}
	return destRef, true
}

// newProvenanceIndex indexes what the destination was already copied from.
//
// It reads the catalog's denormalized CopiedFrom rather than fetching every
// destination snapshot object, which is the whole reason that field exists.
// An entry written by a build predating the field has no provenance to read;
// that degrades to re-copying the snapshot, not to skipping one that was never
// copied, which is the safe direction (RFC 0017 §5.3).
func newProvenanceIndex(entries []SnapshotEntry) provenanceIndex {
	index := provenanceIndex{
		entries: make(map[string]string, len(entries)),
		repoIDs: make(map[string]string, len(entries)),
	}
	for _, entry := range entries {
		prov, ok := core.ParseCopyProvenance(entry.CopiedFrom)
		if !ok {
			continue
		}
		index.entries[prov.SnapshotRef] = entry.Ref
		index.repoIDs[prov.SnapshotRef] = prov.RepoID
	}
	return index
}

// ---------------------------------------------------------------------------
// Per-snapshot rebuild
// ---------------------------------------------------------------------------

func (cm *CopyManager) copySnapshot(ctx context.Context, entry SnapshotEntry, seq int) (string, error) {
	snap, err := cm.loadSourceSnapshot(ctx, entry.Ref)
	if err != nil {
		return "", err
	}

	phase := cm.startPhase(entry)
	defer phase.Done()

	lineage := identityKey(snap.Source)
	prev, incremental := cm.lastCopied[lineage]

	// A tree may only be reused when the source tree it was translated from is
	// known, because reuse is expressed as a diff and a diff is the only way to
	// carry deletions across. Reusing a tree and merely inserting this
	// snapshot's entries over it would leave behind every file deleted since —
	// present in the destination's copy of a snapshot that does not contain it.
	var txn *hamt.Txn
	if incremental {
		txn = cm.dstTree.Edit(prev.destRoot)
		err = cm.applySourceDiff(ctx, txn, prev.sourceRoot, snap.Root, phase)
	} else {
		txn = cm.dstTree.Edit("")
		err = cm.applyWholeTree(ctx, txn, snap.Root, phase)
	}
	if err != nil {
		return "", err
	}

	root, err := txn.Commit(ctx)
	if err != nil {
		return "", fmt.Errorf("commit destination tree: %w", err)
	}

	ref, err := cm.writeSnapshot(ctx, snap, entry.Ref, root, seq)
	if err != nil {
		return "", err
	}
	cm.lastCopied[lineage] = copiedTree{sourceRoot: snap.Root, destRoot: root}
	return ref, nil
}

// applyWholeTree files every entry of a source snapshot into txn. It is the
// path taken for the first snapshot of a lineage in a run, where there is no
// earlier source tree to compare against.
func (cm *CopyManager) applyWholeTree(ctx context.Context, txn *hamt.Txn, root string, phase ui.Phase) error {
	return cm.srcTree.Walk(ctx, root, func(fileID, srcMetaRef string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		copied, err := cm.copyFileMeta(ctx, srcMetaRef)
		if err != nil {
			return fmt.Errorf("filemeta %s: %w", shortRef(srcMetaRef), err)
		}
		phase.Increment(1)
		return txn.Insert(ctx, copied.affinityKey, fileID, copied.ref)
	})
}

// applySourceDiff files only what changed between two consecutive source
// snapshots of one lineage.
//
// Re-filing every entry instead would be correct but quadratic in a way that
// does not show up in read volume, which is why it survived a first round of
// measurement: the remap table already makes re-reading an unchanged file free,
// so the cost hides in the destination tree. Txn.Insert rewrites a leaf entry
// unconditionally, so re-inserting an identical value still dirties the whole
// spine, and Commit then re-serializes and rewrites the tree. Copying 64
// snapshots of a 2000-file tree that way wrote 14.8 MB where the tree itself is
// 3.5 MB — one full rewrite per snapshot, to record a single changed file.
//
// Walking the diff instead makes a run of snapshots cost one tree plus the
// changes between them, which is what backup already does for the same reason.
func (cm *CopyManager) applySourceDiff(
	ctx context.Context, txn *hamt.Txn, oldRoot, newRoot string, phase ui.Phase,
) error {
	return cm.srcTree.Diff(ctx, oldRoot, newRoot, func(entry hamt.DiffEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		phase.Increment(1)

		if entry.NewValue == "" {
			// Removed. The routing key has to come from the metadata the entry
			// used to point at, which the source still holds — every object is
			// content-addressed, so nothing was deleted there.
			key, err := cm.affinityKeyFor(ctx, entry.OldValue)
			if err != nil {
				return fmt.Errorf("filemeta %s: %w", shortRef(entry.OldValue), err)
			}
			return txn.Delete(ctx, key, entry.Key)
		}

		copied, err := cm.copyFileMeta(ctx, entry.NewValue)
		if err != nil {
			return fmt.Errorf("filemeta %s: %w", shortRef(entry.NewValue), err)
		}
		return txn.Insert(ctx, copied.affinityKey, entry.Key, copied.ref)
	})
}

// affinityKeyFor returns the routing key an existing source filemeta was filed
// under, without copying it. A ref already copied in this run is answered from
// the remap table.
func (cm *CopyManager) affinityKeyFor(ctx context.Context, srcMetaRef string) (string, error) {
	if cached, ok := cm.metaRefs[srcMetaRef]; ok {
		return cached.affinityKey, nil
	}
	data, err := cm.get(ctx, srcMetaRef)
	if err != nil {
		return "", err
	}
	var meta core.FileMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("parse filemeta: %w", err)
	}
	return AffinityKey(primaryParentID(&meta), meta.FileID), nil
}

// primeLastCopied recovers, per lineage, a source tree the destination already
// holds a translation of — so the first snapshot of a run can be applied as a
// diff instead of rebuilding a whole tree.
//
// The pairing comes from provenance the destination already records: a copied
// snapshot names the source snapshot it was made from, and the source catalog
// still knows that snapshot's root. Without this, every run would rebuild one
// full tree before it could go incremental, which is the entire cost of a
// scheduled copy that has a single new snapshot to bring over.
//
// A destination snapshot that was not produced by copy has no provenance and is
// skipped: its tree is not a translation of anything, so no diff against it
// would be meaningful. So is one whose recorded source repository is not the
// one being copied from, or whose source snapshot has since been forgotten.
func (cm *CopyManager) primeLastCopied(destEntries, sourceEntries []SnapshotEntry) {
	sourceRoots := make(map[string]string, len(sourceEntries))
	for _, entry := range sourceEntries {
		sourceRoots[entry.Ref] = entry.Snap.Root
	}

	best := map[string]int{}
	for _, entry := range destEntries {
		prov, ok := core.ParseCopyProvenance(entry.CopiedFrom)
		if !ok {
			continue
		}
		if !prov.Matches(core.CopyProvenance{RepoID: cm.src.RepoID, SnapshotRef: prov.SnapshotRef}) {
			continue
		}
		sourceRoot, ok := sourceRoots[prov.SnapshotRef]
		if !ok {
			continue
		}
		lineage := identityKey(entry.Snap.Source)
		if seq, seen := best[lineage]; seen && seq >= entry.Snap.Seq {
			continue
		}
		best[lineage] = entry.Snap.Seq
		cm.lastCopied[lineage] = copiedTree{sourceRoot: sourceRoot, destRoot: entry.Snap.Root}
	}
}

// identityKey collapses a SourceInfo to the lineage it belongs to, preferring
// the stable identity fields and falling back to the display ones for snapshots
// written before those existed.
func identityKey(info *core.SourceInfo) string {
	if info == nil {
		return ""
	}
	container := info.Identity
	if container == "" {
		container = info.Account
	}
	path := info.PathID
	if path == "" {
		path = info.Path
	}
	return info.Type + "\x00" + container + "\x00" + path
}

// writeSnapshot persists the destination snapshot object and catalogs it.
func (cm *CopyManager) writeSnapshot(
	ctx context.Context, srcSnap *core.Snapshot, srcRef, root string, seq int,
) (string, error) {
	prov := core.CopyProvenance{RepoID: cm.src.RepoID, SnapshotRef: srcRef}

	dest := *srcSnap
	dest.Root = root
	// Seq is a global write counter allocated at write time, not part of a
	// snapshot's identity, so it cannot be carried over: the source's value
	// would collide with destination history. See RFC 0017 §4.5.
	dest.Seq = seq
	dest.Meta = prov.ApplyTo(srcSnap.Meta)

	hash, data, err := core.ComputeJSONHash(&dest)
	if err != nil {
		return "", err
	}
	ref := "snapshot/" + hash
	if err := cm.dst.Put(ctx, ref, data); err != nil {
		return "", fmt.Errorf("write snapshot: %w", err)
	}
	AppendSnapshotCatalog(cm.dst, snapshotToSummary(ref, dest), cm.snapLog)
	return ref, nil
}

// ---------------------------------------------------------------------------
// Object rebuild
// ---------------------------------------------------------------------------

// copyFileMeta rewrites one file's metadata for the destination and returns its
// destination ref together with the routing key it must be filed under. Only
// the content reference changes; every other field is carried over byte for
// byte, which is what preserves names, timestamps, ownership and xattrs.
//
// The cache is consulted before the source is read, so a file unchanged across
// a run of snapshots is fetched once no matter how many snapshots contain it.
func (cm *CopyManager) copyFileMeta(ctx context.Context, srcRef string) (copiedMeta, error) {
	if cached, ok := cm.metaRefs[srcRef]; ok {
		return cached, nil
	}

	data, err := cm.get(ctx, srcRef)
	if err != nil {
		return copiedMeta{}, err
	}
	var meta core.FileMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return copiedMeta{}, fmt.Errorf("parse filemeta: %w", err)
	}

	// A folder carries no content, and backup clears its ContentHash rather
	// than recording one for an empty stream.
	if meta.ContentHash != "" && meta.Type != core.FileTypeFolder {
		contentRef, err := cm.copyContent(ctx, &meta)
		if err != nil {
			return copiedMeta{}, err
		}
		meta.ContentRef = contentRef
	}

	destRef, encoded, err := core.FileMetaRef(&meta)
	if err != nil {
		return copiedMeta{}, err
	}
	if err := cm.putIfMissing(ctx, destRef, encoded); err != nil {
		return copiedMeta{}, err
	}

	copied := copiedMeta{ref: destRef, affinityKey: AffinityKey(primaryParentID(&meta), meta.FileID)}
	cm.metaRefs[srcRef] = copied
	return copied, nil
}

// copyContent rebuilds one content manifest and returns its destination
// reference (the bare hex ref, as stored on FileMeta.ContentRef).
//
// The destination reference is derived from the plaintext content hash the
// filemeta already records, so it does not have to be recomputed from the
// stream. What does have to be rebuilt is the manifest body: it lists chunk
// refs, and those are named under the destination's key.
func (cm *CopyManager) copyContent(ctx context.Context, meta *core.FileMeta) (string, error) {
	destContentRef := meta.ContentHash
	if len(cm.dstHMAC) > 0 {
		destContentRef = crypto.ComputeHMAC(cm.dstHMAC, []byte(meta.ContentHash))
	}

	srcKey := "content/" + meta.ContentRef
	if meta.ContentRef == "" {
		srcKey = "content/" + meta.ContentHash
	}
	if _, done := cm.contentRefs[srcKey]; done {
		return destContentRef, nil
	}

	destKey := "content/" + destContentRef
	exists, err := cm.dst.Exists(ctx, destKey)
	if err != nil {
		return "", err
	}
	if exists {
		// Already present, either from an interrupted earlier run or because
		// the destination independently holds this exact content. Either way
		// its chunks are present too, so there is nothing to transfer.
		cm.contentRefs[srcKey] = destContentRef
		return destContentRef, nil
	}

	raw, err := cm.get(ctx, srcKey)
	if err != nil {
		return "", err
	}
	var content core.Content
	if err := json.Unmarshal(raw, &content); err != nil {
		return "", fmt.Errorf("parse content %s: %w", shortRef(srcKey), err)
	}

	if len(content.Chunks) > 0 {
		rebuilt := make([]string, len(content.Chunks))
		for i, chunkRef := range content.Chunks {
			destChunkRef, err := cm.copyChunk(ctx, chunkRef)
			if err != nil {
				return "", err
			}
			rebuilt[i] = destChunkRef
		}
		content.Chunks = rebuilt
	}

	encoded, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	if err := cm.dst.Put(ctx, destKey, encoded); err != nil {
		return "", err
	}
	cm.contentRefs[srcKey] = destContentRef
	return destContentRef, nil
}

// copyChunk transfers one chunk, re-addressing it under the destination's key.
//
// Chunk boundaries are reused rather than recomputed. FastCDC here is
// parameterised by fixed constants with no key-derived seed, so re-running the
// chunker over the same plaintext is guaranteed to reproduce the split the
// source already recorded — and identical plaintext therefore lands on the same
// destination ref as data backed up directly, which is what lets a copy
// deduplicate against content the destination already holds (RFC 0017 §4.3).
func (cm *CopyManager) copyChunk(ctx context.Context, srcRef string) (string, error) {
	if cached, ok := cm.chunkRefs[srcRef]; ok {
		return cached, nil
	}

	data, err := cm.get(ctx, srcRef)
	if err != nil {
		return "", fmt.Errorf("read chunk %s: %w", shortRef(srcRef), err)
	}

	destRef := "chunk/" + core.ComputeHash(data)
	if len(cm.dstHMAC) > 0 {
		destRef = "chunk/" + crypto.ComputeHMAC(cm.dstHMAC, data)
	}
	if err := cm.putIfMissing(ctx, destRef, data); err != nil {
		return "", err
	}
	cm.chunkRefs[srcRef] = destRef
	return destRef, nil
}

// ---------------------------------------------------------------------------
// index/latest
// ---------------------------------------------------------------------------

// reconcileLatest points index/latest at the newest copied snapshot, but only
// when that is newer than what the destination already considered latest.
//
// This is the one place copy departs from the repository's "highest Seq wins"
// rule, and it has to. Copied snapshots are allocated sequence numbers in write
// order, so the last one written always holds the highest Seq — which under the
// ordinary rule would make a copy of old history silently displace a live head
// that is years newer.
func (cm *CopyManager) reconcileLatest(ctx context.Context, copied []CopiedSnapshot) error {
	if len(copied) == 0 {
		return nil
	}
	newest := copied[len(copied)-1]
	newestAt, err := time.Parse(time.RFC3339, newest.Created)
	if err != nil {
		// An unparseable timestamp is not a reason to fail a completed copy,
		// but it is a reason not to move a pointer based on it.
		cm.log.Debugf("not moving index/latest: unparseable created %q", newest.Created)
		return nil
	}

	currentRef, _, err := resolveLatestContext(ctx, cm.dst)
	if err != nil {
		return err
	}
	if currentRef != "" && currentRef != newest.DestRef {
		current, err := loadSnapshotByRef(ctx, cm.dst, currentRef)
		if err != nil {
			return err
		}
		if at, err := time.Parse(time.RFC3339, current.Created); err == nil && !newestAt.After(at) {
			cm.log.Debugf("leaving index/latest at %s: newer than every copied snapshot", shortRef(currentRef))
			return nil
		}
	}

	destSnap, err := loadSnapshotByRef(ctx, cm.dst, newest.DestRef)
	if err != nil {
		return err
	}
	return updateLatest(cm.dst, newest.DestRef, destSnap.Seq)
}

// nextSeq returns the sequence number the first copied snapshot should take.
func (cm *CopyManager) nextSeq() (int, error) {
	_, seq, err := resolveLatest(cm.dst)
	if err != nil {
		return 0, err
	}
	return seq + 1, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (cm *CopyManager) get(ctx context.Context, key string) ([]byte, error) {
	data, err := cm.src.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	cm.bytesRead += int64(len(data))
	return data, nil
}

// putIfMissing writes key only when the destination does not already hold it.
//
// The Exists check is what makes an interrupted copy cheap to retry: every
// object below the snapshot is content-addressed, so a rerun derives the same
// destination refs and finds most of its work already done.
func (cm *CopyManager) putIfMissing(ctx context.Context, key string, data []byte) error {
	exists, err := cm.dst.Exists(ctx, key)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return cm.dst.Put(ctx, key, data)
}

func (cm *CopyManager) loadSourceSnapshot(ctx context.Context, ref string) (*core.Snapshot, error) {
	data, err := cm.get(ctx, ref)
	if err != nil {
		return nil, err
	}
	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parse snapshot %s: %w", shortRef(ref), err)
	}
	return &snap, nil
}

func (cm *CopyManager) startPhase(entry SnapshotEntry) ui.Phase {
	if cm.reporter == nil {
		cm.reporter = ui.NewNoOpReporter()
	}
	label := fmt.Sprintf("Copying snapshot %s", shortRef(entry.Ref))
	if entry.Snap.Source != nil && entry.Snap.Source.Path != "" {
		label = fmt.Sprintf("Copying %s (%s)", shortRef(entry.Ref), entry.Snap.Source.Path)
	}
	return cm.reporter.StartPhase(label, 0, false)
}

func shortRef(ref string) string {
	hash := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		hash = ref[i+1:]
	}
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
