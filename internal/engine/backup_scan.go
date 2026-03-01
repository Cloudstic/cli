package engine

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// scanSource chooses the right scan strategy (full or incremental) and returns
// the new HAMT root, files pending upload, and a change token for the next run.
func (bm *BackupManager) scanSource(ctx context.Context, oldRoot, changeToken string) (newRoot string, pending []core.FileMeta, totalBytes int64, newToken string, usedFullScan bool, err error) {
	incSrc, isIncremental := bm.source.(store.IncrementalSource)
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

func (bm *BackupManager) processEntry(ctx context.Context, meta *core.FileMeta, oldRoot string, s *scanState, phase ui.Phase) error {
	if meta.Type == core.FileTypeFolder {
		meta.ContentHash = ""
		meta.Size = 0
	}

	changed, oldRef, err := bm.detectChange(oldRoot, meta)
	if err != nil {
		return err
	}

	if !changed {
		bm.recordStat(meta.Type, false, false)
		s.root, err = bm.tree.Insert(s.root, meta.FileID, oldRef)
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

func (bm *BackupManager) scanIncremental(ctx context.Context, oldRoot string, incSrc store.IncrementalSource, token string) (newRoot string, pending []core.FileMeta, totalBytes int64, newToken string, err error) {
	phase := bm.reporter.StartPhase("Scanning (incremental)", 0, false)
	s := &scanState{root: oldRoot}

	newToken, walkErr := incSrc.WalkChanges(ctx, token, func(fc store.FileChange) error {
		phase.Increment(1)

		switch fc.Type {
		case store.ChangeDelete:
			bm.recordRemoved(fc.Meta.Type)
			s.root, err = bm.tree.Delete(s.root, fc.Meta.FileID)
			if err != nil {
				return fmt.Errorf("hamt delete %s: %w", fc.Meta.FileID, err)
			}
		case store.ChangeUpsert:
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
	return bm.tree.Insert(root, meta.FileID, metaRef)
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
	workers := min(20, len(bm.pendingMetas))

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
