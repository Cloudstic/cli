package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/source"
	"github.com/cloudstic/cli/pkg/store"
)

// scanSource chooses the right scan strategy (full or incremental) and returns
// the new HAMT root, files pending upload, and a change token for the next run.
// scanSource chooses the right scan strategy and opens bm.txn over the right
// base tree: an incremental scan edits the previous snapshot's tree in place,
// while a full scan rebuilds one from nothing.
func (bm *BackupManager) scanSource(ctx context.Context, oldRoot, changeToken string) (pending []core.FileMeta, totalBytes int64, newToken string, usedFullScan bool, err error) {
	incSrc, isIncremental := bm.source.(source.IncrementalSource)
	if isIncremental && changeToken != "" {
		pending, totalBytes, newToken, err = bm.scanIncremental(ctx, oldRoot, incSrc, changeToken)
		return pending, totalBytes, newToken, false, err
	}

	if isIncremental {
		newToken, err = incSrc.GetStartPageToken()
		if err != nil {
			return nil, 0, "", false, fmt.Errorf("get start page token: %w", err)
		}
	}

	pending, totalBytes, err = bm.scan(ctx, oldRoot)
	return pending, totalBytes, newToken, true, err
}

type scanState struct {
	pending    []core.FileMeta
	totalBytes int64
}

// primaryParentID returns the raw source-level parent identifier for a FileMeta.
// This is the first element of meta.Parents, which contains raw source IDs (e.g. GDrive folder IDs).
// Returns "" for root-level entries with no parents.
func primaryParentID(meta *core.FileMeta) string {
	if len(meta.Parents) > 0 {
		return meta.Parents[0]
	}
	return ""
}

func (bm *BackupManager) processEntry(ctx context.Context, meta *core.FileMeta, oldRef string, s *scanState, phase ui.Phase) error {
	if meta.Type == core.FileTypeFolder {
		meta.ContentHash = ""
		meta.Size = 0
	}

	// Record this entry's parent so lookupMetaByFileID can use AffinityKey
	// rather than walking the tree. Only scanIncremental allocates the index,
	// since its changes are the ones that routinely arrive without Paths; a full
	// scan skips the write instead of running it once per file for a map whose
	// reader it does not reach. A nil index costs a slower lookup at most — see
	// the field comment on BackupManager.
	if bm.parentIndex != nil {
		bm.parentIndex[meta.FileID] = primaryParentID(meta)
	}

	// Resolve Paths when the source hasn't populated it (incremental/changes
	// sources only emit changed entries and can't build a full path map).
	if len(meta.Paths) == 0 {
		meta.Paths = []string{bm.buildPathFromTree(ctx, meta)}
	}

	changed, err := bm.compareWithOld(ctx, oldRef, meta)
	if err != nil {
		return err
	}

	if !changed {
		bm.recordStat(meta.Type, false, false)
		if err := bm.txn.Insert(ctx, AffinityKey(primaryParentID(meta), meta.FileID), meta.FileID, oldRef); err != nil {
			return fmt.Errorf("hamt insert: %w", err)
		}
		return nil
	}

	bm.recordStat(meta.Type, true, oldRef == "")

	if meta.Type == core.FileTypeFolder {
		return bm.insertFolder(ctx, meta, phase)
	}

	phase.Logf(ui.DetailVerbose, "Queueing: %s", meta.Name)
	s.pending = append(s.pending, *meta)
	s.totalBytes += meta.Size
	return nil
}

func (bm *BackupManager) scan(ctx context.Context, oldRoot string) (pending []core.FileMeta, totalBytes int64, err error) {
	phase := bm.reporter.StartPhase("Scanning", 0, false)
	bm.txn = bm.tree.Edit("")
	s := &scanState{}

	batch := make([]core.FileMeta, 0, entryBatch)
	err = bm.source.Walk(ctx, func(meta core.FileMeta) error {
		phase.Increment(1)
		batch = append(batch, meta)
		if len(batch) < entryBatch {
			return nil
		}
		if err := bm.processBatch(ctx, oldRoot, batch, s, phase); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	})
	if err == nil && len(batch) > 0 {
		err = bm.processBatch(ctx, oldRoot, batch, s, phase)
	}

	if err != nil {
		phase.Error()
		return nil, 0, err
	}
	phase.Done()
	return s.pending, s.totalBytes, nil
}

// processBatch resolves a buffered run of entries against the previous snapshot
// and then processes them, in the order the source walked them.
//
// The two halves are separate so the reads can be declared. Change detection
// loads the previous filemeta of every entry that has one, and a source walk
// visits them in an order that has nothing to do with where they were stored: a
// file's filemeta lives in whichever packfile was open the last time that file
// changed, so after eighty backups an unchanged tree's filemetas are spread
// across every pack the repository has. Read one at a time in walk order, that
// is a packfile contacted, dropped and contacted again — at 82 packs, 791 of
// backup's 985 requests. Resolving the refs first lets the whole batch be
// declared at once, so the store reads each bundle's share of it together.
//
// Processing still happens in walk order, not in the store's. It is the reads
// that are reordered, and only those: the entries a scan queues become the
// upload order, which is what gives newly written objects their own locality —
// reordering that to match where the *previous* snapshot's metadata happens to
// live would trade a one-time read win for a permanent write regression.
func (bm *BackupManager) processBatch(ctx context.Context, oldRoot string, batch []core.FileMeta, s *scanState, phase ui.Phase) error {
	oldRefs := make([]string, len(batch))
	for i := range batch {
		ref, err := bm.lookupOldRef(ctx, oldRoot, &batch[i])
		if err != nil {
			return err
		}
		oldRefs[i] = ref
	}

	if err := bm.prefetchOldMetas(ctx, oldRefs); err != nil {
		return err
	}

	for i := range batch {
		if err := bm.processEntry(ctx, &batch[i], oldRefs[i], s, phase); err != nil {
			return err
		}
	}
	return nil
}

// prefetchOldMetas reads the batch's previous filemetas in the order the store
// nominates, leaving them in the loader's cache for the pass that follows.
//
// The declaration is exact, which is what makes it worth making. Every ref
// handed over here is read by compareWithOld moments later, so the store is not
// told to fetch anything the scan then skips; and a ref cannot repeat within a
// batch, because a filemeta names its own FileID and two entries cannot share
// one. Backup's loader memoizes, so reading here and reading again below is one
// store read, not two.
func (bm *BackupManager) prefetchOldMetas(ctx context.Context, oldRefs []string) error {
	refs := make([]string, 0, len(oldRefs))
	for _, ref := range oldRefs {
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return readGrouped(ctx, bm.store, refs, func(ref string) error {
		_, err := bm.metas.load(ctx, ref)
		return err
	})
}

func (bm *BackupManager) scanIncremental(ctx context.Context, oldRoot string, incSrc source.IncrementalSource, token string) (pending []core.FileMeta, totalBytes int64, newToken string, err error) {
	phase := bm.reporter.StartPhase("Scanning (incremental)", 0, false)
	bm.txn = bm.tree.Edit(oldRoot)
	// A change may legitimately arrive with no Paths, so this is the one scan
	// strategy whose entries reach lookupMetaByFileID and can use the index.
	bm.parentIndex = make(map[string]string)
	s := &scanState{}

	newToken, walkErr := incSrc.WalkChanges(ctx, token, func(fc source.FileChange) error {
		phase.Increment(1)

		switch fc.Type {
		case source.ChangeDelete:
			bm.recordRemoved(fc.Meta.Type)
			deleteParentID := primaryParentID(&fc.Meta)
			if deleteParentID == "" {
				deleteParentID, err = bm.lookupDeleteParentID(ctx, fc.Meta.FileID)
				if err != nil {
					return err
				}
			}
			if err := bm.txn.Delete(ctx, AffinityKey(deleteParentID, fc.Meta.FileID), fc.Meta.FileID); err != nil {
				return fmt.Errorf("hamt delete %s: %w", fc.Meta.FileID, err)
			}
		case source.ChangeUpsert:
			// Not batched, unlike the full scan. A change feed emits only what
			// changed, so there is rarely a batch's worth to group, and buffering
			// upserts across the deletes interleaved with them would reorder two
			// operations on the same entry against each other.
			oldRef, err := bm.lookupOldRef(ctx, oldRoot, &fc.Meta)
			if err != nil {
				return err
			}
			return bm.processEntry(ctx, &fc.Meta, oldRef, s, phase)
		}
		return nil
	})

	if walkErr != nil {
		phase.Error()
		return nil, 0, "", walkErr
	}

	phase.Done()
	return s.pending, s.totalBytes, newToken, nil
}

func (bm *BackupManager) lookupDeleteParentID(ctx context.Context, fileID string) (string, error) {
	ref, err := bm.txn.LookupByKey(ctx, fileID)
	if err != nil {
		return "", fmt.Errorf("lookup old file for delete %s: %w", fileID, err)
	}
	if ref == "" {
		return "", nil
	}

	oldMeta, err := bm.metas.load(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("load old file metadata for delete %s: %w", fileID, err)
	}
	return primaryParentID(oldMeta), nil
}

// lookupOldRef finds meta's entry in the previous snapshot, returning an empty
// ref when the entry is new.
//
// It is separate from the comparison because it is the half that can be hoisted
// out of the walk: it reads the previous snapshot's tree, which no part of the
// current scan modifies, so a batch of entries may resolve all of its refs up
// front. That is what lets scan declare a batch's filemeta reads before making
// any of them — see prefetchOldMetas.
func (bm *BackupManager) lookupOldRef(ctx context.Context, oldRoot string, meta *core.FileMeta) (string, error) {
	ref, err := bm.tree.Lookup(ctx, oldRoot, AffinityKey(primaryParentID(meta), meta.FileID), meta.FileID)
	if err != nil {
		return "", fmt.Errorf("hamt lookup: %w", err)
	}
	return ref, nil
}

// compareWithOld compares meta against the entry it had in the previous
// snapshot, named by oldRef. An empty oldRef means the entry is new.
//
// For sources that do not provide a content hash (e.g. Google Drive), a
// fast-path compares observable metadata and carries the hash forward to avoid
// false-positive diffs.
func (bm *BackupManager) compareWithOld(ctx context.Context, oldRef string, meta *core.FileMeta) (changed bool, err error) {
	if oldRef == "" {
		return true, nil
	}

	oldMeta, err := bm.metas.load(ctx, oldRef)
	if err != nil {
		return false, err
	}

	// Native Google files: use headRevisionId as the sole change signal.
	// Size and ContentHash comparisons are unreliable for exported files
	// (see RFC 0003 section 2.4).
	if isGoogleNativeMeta(meta) {
		newRevID, _ := meta.Extra["headRevisionId"].(string)
		oldRevID, _ := oldMeta.Extra["headRevisionId"].(string)
		if newRevID != "" && newRevID == oldRevID {
			meta.ContentHash = oldMeta.ContentHash
			meta.ContentRef = oldMeta.ContentRef
			meta.Size = oldMeta.Size
			return false, nil
		}
		return true, nil
	}

	if meta.ContentHash == "" && oldMeta.ContentHash != "" && metadataEqual(*meta, *oldMeta) {
		meta.ContentHash = oldMeta.ContentHash
		meta.ContentRef = oldMeta.ContentRef
	} else if meta.ContentHash != "" && meta.ContentHash == oldMeta.ContentHash && meta.ContentRef == "" {
		// Source provides a hash directly. If the old meta already has a ContentRef
		// (written by a previous backup that introduced HMAC), carry it forward so
		// Ref() produces the same key and the entry is not falsely detected as changed.
		meta.ContentRef = oldMeta.ContentRef
	}

	newPersisted := persistedFileMeta(*meta)
	newRef, _, err := core.FileMetaRef(&newPersisted)
	if err != nil {
		return false, err
	}
	return newRef != oldRef, nil
}

// isGoogleNativeMeta returns true if the FileMeta represents a Google-native
// file (Docs, Sheets, etc.) based on the stored mimeType in Extra.
func isGoogleNativeMeta(meta *core.FileMeta) bool {
	if meta.Extra == nil {
		return false
	}
	mimeType, _ := meta.Extra["mimeType"].(string)
	return strings.HasPrefix(mimeType, "application/vnd.google-apps.") &&
		mimeType != "application/vnd.google-apps.folder"
}

func metadataEqual(a, b core.FileMeta) bool {
	return a.Name == b.Name &&
		a.Size == b.Size &&
		a.Mtime == b.Mtime &&
		a.Type == b.Type &&
		a.Mode == b.Mode &&
		a.Uid == b.Uid &&
		a.Gid == b.Gid &&
		a.Btime == b.Btime &&
		a.Flags == b.Flags &&
		xattrsEqual(a.Xattrs, b.Xattrs) &&
		len(a.Parents) == len(b.Parents)
}

func xattrsEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || !bytesEqual(v, bv) {
			return false
		}
	}
	return true
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (bm *BackupManager) insertFolder(ctx context.Context, meta *core.FileMeta, phase ui.Phase) error {
	phase.Logf(ui.DetailVerbose, "Folder: %s (New/Changed)", meta.Name)
	meta.ContentHash = ""
	meta.Size = 0

	persisted := persistedFileMeta(*meta)
	metaRef, metaData, err := core.FileMetaRef(&persisted)
	if err != nil {
		return err
	}
	if !bm.metas.cached(metaRef) {
		bm.pendingMetas[metaRef] = metaData
	}
	bm.trackFileMeta(metaRef, *meta)
	return bm.txn.Insert(ctx, AffinityKey(primaryParentID(meta), meta.FileID), meta.FileID, metaRef)
}

func (bm *BackupManager) flushPendingMetas(ctx context.Context) error {
	if len(bm.pendingMetas) == 0 {
		return nil
	}

	type job struct {
		ref  string
		data []byte
	}

	jobs := make(chan job, len(bm.pendingMetas))
	errs := make(chan error, len(bm.pendingMetas))
	workers := min(store.GetConcurrencyHint(bm.store, 20), len(bm.pendingMetas))

	for range workers {
		go func() {
			for j := range jobs {
				errs <- bm.store.Put(ctx, j.ref, j.data)
			}
		}()
	}

	for ref, data := range bm.pendingMetas {
		jobs <- job{ref: ref, data: data}
	}
	close(jobs)

	for range bm.pendingMetas {
		if err := <-errs; err != nil {
			return fmt.Errorf("flush folder metadata: %w", err)
		}
	}

	bm.pendingMetas = make(map[string][]byte)
	return nil
}

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

// buildPathFromTree reconstructs the full path for a FileMeta entry by walking
// the parent chain in the HAMT tree. This is used for incremental/changes
// sources that can't build a path map (the parent may not be in the change set).
func (bm *BackupManager) buildPathFromTree(ctx context.Context, meta *core.FileMeta) string {
	return fileMetaPath(*meta, func(parentID string) (core.FileMeta, bool) {
		parent := bm.lookupMetaByFileID(ctx, parentID)
		if parent == nil {
			return core.FileMeta{}, false
		}
		return *parent, true
	})
}

// lookupMetaByFileID resolves a FileID to its FileMeta via the HAMT tree.
// It checks newMetas (just inserted this scan) first, then falls back to the store.
// Uses parentIndex to resolve the affinity routing key; falls back to a full-tree walk
// for entries not yet seen in this scan (e.g. incremental backups), and for every
// entry on the full-scan path, where the index is nil because nothing populates it.
func (bm *BackupManager) lookupMetaByFileID(ctx context.Context, fileID string) *core.FileMeta {
	parentID := bm.parentIndex[fileID]
	ref, err := bm.txn.Lookup(ctx, AffinityKey(parentID, fileID), fileID)
	if err != nil || ref == "" {
		// parentID not in index (e.g. entry from a previous snapshot not re-scanned);
		// fall back to a walk-based lookup.
		ref, err = bm.txn.LookupByKey(ctx, fileID)
		if err != nil || ref == "" {
			return nil
		}
	}
	if fm, ok := bm.newMetas[ref]; ok {
		return &fm
	}
	fm, err := bm.metas.load(ctx, ref)
	if err != nil {
		return nil
	}
	return fm
}

// countRemoved uses a structural HAMT diff to count entries present in oldRoot
// but absent from newRoot (full-scan path where deletions are implicit).
func (bm *BackupManager) countRemoved(ctx context.Context, oldRoot string) error {
	if oldRoot == "" {
		return nil
	}
	return bm.txn.DiffFrom(ctx, oldRoot, func(d hamt.DiffEntry) error {
		if d.OldValue != "" && d.NewValue == "" {
			meta, err := bm.metas.load(ctx, d.OldValue)
			if err != nil {
				return err
			}
			bm.recordRemoved(meta.Type)
		}
		return nil
	})
}
