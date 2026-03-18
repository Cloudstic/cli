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
func (bm *BackupManager) scanSource(ctx context.Context, oldRoot, changeToken string) (newRoot string, pending []core.FileMeta, totalBytes int64, newToken string, usedFullScan bool, err error) {
	incSrc, isIncremental := bm.source.(source.IncrementalSource)
	if isIncremental && changeToken != "" {
		newRoot, pending, totalBytes, newToken, err = bm.scanIncremental(ctx, oldRoot, incSrc, changeToken)
		return newRoot, pending, totalBytes, newToken, false, err
	}

	if isIncremental {
		newToken, err = incSrc.GetStartPageToken()
		if err != nil {
			return "", nil, 0, "", false, fmt.Errorf("get start page token: %w", err)
		}
	}

	newRoot, pending, totalBytes, err = bm.scan(ctx, oldRoot)
	return newRoot, pending, totalBytes, newToken, true, err
}

type scanState struct {
	root       string
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

func (bm *BackupManager) processEntry(ctx context.Context, meta *core.FileMeta, oldRoot string, s *scanState, phase ui.Phase) error {
	if meta.Type == core.FileTypeFolder {
		meta.ContentHash = ""
		meta.Size = 0
	}

	// Record this entry's parent so lookupMetaByFileID can use AffinityKey.
	bm.parentIndex[meta.FileID] = primaryParentID(meta)

	// Resolve Paths when the source hasn't populated it (incremental/changes
	// sources only emit changed entries and can't build a full path map).
	if len(meta.Paths) == 0 {
		meta.Paths = []string{bm.buildPathFromTree(ctx, s.root, meta)}
	}

	changed, oldRef, err := bm.detectChange(ctx, oldRoot, meta)
	if err != nil {
		return err
	}

	if !changed {
		bm.recordStat(meta.Type, false, false)
		s.root, err = bm.tree.Insert(s.root, primaryParentID(meta), meta.FileID, oldRef)
		if err != nil {
			return fmt.Errorf("hamt insert: %w", err)
		}
		return nil
	}

	bm.recordStat(meta.Type, true, oldRef == "")

	if meta.Type == core.FileTypeFolder {
		s.root, err = bm.insertFolder(ctx, s.root, meta, phase)
		return err
	}

	if bm.cfg.verbose {
		phase.Log(fmt.Sprintf("Queueing: %s", meta.Name))
	}
	s.pending = append(s.pending, *meta)
	s.totalBytes += meta.Size
	return nil
}

func (bm *BackupManager) scan(ctx context.Context, oldRoot string) (newRoot string, pending []core.FileMeta, totalBytes int64, err error) {
	phase := bm.reporter.StartPhase("Scanning", 0, false)
	s := &scanState{}

	err = bm.source.Walk(ctx, func(meta core.FileMeta) error {
		phase.Increment(1)
		return bm.processEntry(ctx, &meta, oldRoot, s, phase)
	})

	if err != nil {
		phase.Error()
		return "", nil, 0, err
	}
	phase.Done()
	return s.root, s.pending, s.totalBytes, nil
}

func (bm *BackupManager) scanIncremental(ctx context.Context, oldRoot string, incSrc source.IncrementalSource, token string) (newRoot string, pending []core.FileMeta, totalBytes int64, newToken string, err error) {
	phase := bm.reporter.StartPhase("Scanning (incremental)", 0, false)
	s := &scanState{root: oldRoot}

	newToken, walkErr := incSrc.WalkChanges(ctx, token, func(fc source.FileChange) error {
		phase.Increment(1)

		switch fc.Type {
		case source.ChangeDelete:
			bm.recordRemoved(fc.Meta.Type)
			deleteParentID := primaryParentID(&fc.Meta)
			if deleteParentID == "" {
				deleteParentID, err = bm.lookupDeleteParentID(ctx, s.root, fc.Meta.FileID)
				if err != nil {
					return err
				}
			}
			s.root, err = bm.tree.Delete(s.root, deleteParentID, fc.Meta.FileID)
			if err != nil {
				return fmt.Errorf("hamt delete %s: %w", fc.Meta.FileID, err)
			}
		case source.ChangeUpsert:
			return bm.processEntry(ctx, &fc.Meta, oldRoot, s, phase)
		}
		return nil
	})

	if walkErr != nil {
		phase.Error()
		return "", nil, 0, "", walkErr
	}

	phase.Done()
	return s.root, s.pending, s.totalBytes, newToken, nil
}

func (bm *BackupManager) lookupDeleteParentID(ctx context.Context, root, fileID string) (string, error) {
	if root == "" {
		return "", nil
	}

	ref, err := bm.tree.LookupByFileID(root, fileID)
	if err != nil {
		return "", fmt.Errorf("lookup old file for delete %s: %w", fileID, err)
	}
	if ref == "" {
		return "", nil
	}

	oldMeta, err := bm.loadMeta(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("load old file metadata for delete %s: %w", fileID, err)
	}
	return primaryParentID(oldMeta), nil
}

// detectChange compares meta against the previous snapshot. It returns whether
// the entry changed, and the old value ref (empty when the entry is new).
//
// For sources that do not provide a content hash (e.g. Google Drive), a
// fast-path compares observable metadata and carries the hash forward to avoid
// false-positive diffs.
func (bm *BackupManager) detectChange(ctx context.Context, oldRoot string, meta *core.FileMeta) (changed bool, oldRef string, err error) {
	oldRef, err = bm.tree.Lookup(oldRoot, primaryParentID(meta), meta.FileID)
	if err != nil {
		return false, "", fmt.Errorf("hamt lookup: %w", err)
	}
	if oldRef == "" {
		return true, "", nil
	}

	oldMeta, err := bm.loadMeta(ctx, oldRef)
	if err != nil {
		return false, "", err
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
			return false, oldRef, nil
		}
		return true, oldRef, nil
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

	newRef, _, err := meta.Ref()
	if err != nil {
		return false, "", err
	}
	return newRef != oldRef, oldRef, nil
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

func (bm *BackupManager) insertFolder(_ context.Context, root string, meta *core.FileMeta, phase ui.Phase) (string, error) {
	if bm.cfg.verbose {
		phase.Log(fmt.Sprintf("Folder: %s (New/Changed)", meta.Name))
	}
	meta.ContentHash = ""
	meta.Size = 0

	metaRef, metaData, err := meta.Ref()
	if err != nil {
		return "", err
	}
	bm.metaCacheMu.RLock()
	_, inCache := bm.metaCache[metaRef]
	bm.metaCacheMu.RUnlock()
	if !inCache {
		bm.pendingMetas[metaRef] = metaData
	}
	bm.trackFileMeta(metaRef, *meta)
	return bm.tree.Insert(root, primaryParentID(meta), meta.FileID, metaRef)
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
func (bm *BackupManager) buildPathFromTree(ctx context.Context, root string, meta *core.FileMeta) string {
	const maxDepth = 50
	parts := []string{meta.Name}
	curParents := meta.Parents
	for i := 0; i < maxDepth && len(curParents) > 0; i++ {
		parent := bm.lookupMetaByFileID(ctx, root, curParents[0])
		if parent == nil {
			break
		}
		// Short-circuit: if parent already has a resolved path, prepend it.
		if len(parent.Paths) > 0 {
			return parent.Paths[0] + "/" + strings.Join(parts, "/")
		}
		parts = append(parts, parent.Name)
		curParents = parent.Parents
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "/")
}

// lookupMetaByFileID resolves a FileID to its FileMeta via the HAMT tree.
// It checks newMetas (just inserted this scan) first, then falls back to the store.
// Uses parentIndex to resolve the AffinityKey; falls back to a full-tree walk
// for entries not yet seen in this scan (e.g. incremental backups).
func (bm *BackupManager) lookupMetaByFileID(ctx context.Context, root, fileID string) *core.FileMeta {
	parentID := bm.parentIndex[fileID]
	ref, err := bm.tree.Lookup(root, parentID, fileID)
	if err != nil || ref == "" {
		// parentID not in index (e.g. entry from a previous snapshot not re-scanned);
		// fall back to a walk-based lookup.
		ref, err = bm.tree.LookupByFileID(root, fileID)
		if err != nil || ref == "" {
			return nil
		}
	}
	if fm, ok := bm.newMetas[ref]; ok {
		return &fm
	}
	fm, err := bm.loadMeta(ctx, ref)
	if err != nil {
		return nil
	}
	return fm
}

// countRemoved uses a structural HAMT diff to count entries present in oldRoot
// but absent from newRoot (full-scan path where deletions are implicit).
func (bm *BackupManager) countRemoved(ctx context.Context, oldRoot, newRoot string) error {
	if oldRoot == "" {
		return nil
	}
	return bm.tree.Diff(oldRoot, newRoot, func(d hamt.DiffEntry) error {
		if d.OldValue != "" && d.NewValue == "" {
			meta, err := bm.loadMeta(ctx, d.OldValue)
			if err != nil {
				return err
			}
			bm.recordRemoved(meta.Type)
		}
		return nil
	})
}
