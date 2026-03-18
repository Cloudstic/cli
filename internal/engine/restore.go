package engine

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// RestoreOption configures a restore operation.
type RestoreOption func(*restoreConfig)

type restoreConfig struct {
	dryRun     bool
	verbose    bool
	pathFilter string
}

type restorePlan struct {
	cfg         restoreConfig
	sorted      []core.FileMeta
	byID        map[string]core.FileMeta
	snapshotRef string
	root        string
}

// WithRestoreDryRun resolves the snapshot and reports what would be restored without writing output.
func WithRestoreDryRun() RestoreOption {
	return func(cfg *restoreConfig) { cfg.dryRun = true }
}

// WithRestoreVerbose logs each file/dir being written.
func WithRestoreVerbose() RestoreOption {
	return func(cfg *restoreConfig) { cfg.verbose = true }
}

// WithRestorePath limits the restore to files matching the given path.
// If the path ends with "/", all files under that subtree are included.
// Otherwise, only the file with the exact path is restored.
func WithRestorePath(p string) RestoreOption {
	return func(cfg *restoreConfig) { cfg.pathFilter = p }
}

// RestoreResult holds the outcome of a restore operation.
type RestoreResult struct {
	SnapshotRef  string
	Root         string
	FilesWritten int
	DirsWritten  int
	BytesWritten int64
	Errors       int
	Warnings     int
	DryRun       bool
}

// RestoreWriter is the output abstraction for restore formats.
type RestoreWriter interface {
	MkdirAll(path string, meta core.FileMeta) error
	WriteFile(path string, meta core.FileMeta, writeContent func(io.Writer) error) error
	BytesWritten() int64
	Close() error
}

// RestoreManager recreates a snapshot's file tree using a RestoreWriter output format.
type RestoreManager struct {
	store     store.ObjectStore
	tree      *hamt.Tree
	reporter  ui.Reporter
	metaCache map[string]core.FileMeta
}

func NewRestoreManager(s store.ObjectStore, reporter ui.Reporter) *RestoreManager {
	return &RestoreManager{
		store:    s,
		tree:     hamt.NewTree(hamt.NewTransactionalStore(s)),
		reporter: reporter,
	}
}

// Run restores the snapshot's file tree to the provided writer format.
// snapshotRef can be "", "latest", a bare hash, or "snapshot/<hash>".
func (rm *RestoreManager) Run(ctx context.Context, writer RestoreWriter, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	lock, err := AcquireSharedLock(ctx, rm.store, "restore")
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	plan, err := rm.prepareRestore(ctx, snapshotRef, opts...)
	if err != nil {
		return nil, err
	}

	if plan.cfg.dryRun {
		return rm.dryRunRestore(plan.sorted, plan.byID, plan.snapshotRef, plan.root), nil
	}
	if writer == nil {
		return nil, fmt.Errorf("restore writer is required")
	}
	return rm.runWithWriter(ctx, plan, writer)
}

func (rm *RestoreManager) runWithWriter(ctx context.Context, plan restorePlan, writer RestoreWriter) (*RestoreResult, error) {
	result := &RestoreResult{SnapshotRef: plan.snapshotRef, Root: plan.root}
	phase := rm.reporter.StartPhase("Restoring", int64(len(plan.sorted)), false)
	if setter, ok := writer.(restoreWarningSetter); ok {
		setter.SetWarningFunc(func(msg string) {
			result.Warnings++
			phase.Log("Warning: " + msg)
		})
	}
	for _, meta := range plan.sorted {
		rel := buildRestorePath(meta, plan.byID)

		if meta.Type == core.FileTypeFolder {
			if err := writer.MkdirAll(rel, meta); err != nil {
				if err == errRestoreSkipped {
					phase.Increment(1)
					continue
				}
				phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
				result.Errors++
				phase.Increment(1)
				continue
			}
			if plan.cfg.verbose {
				phase.Log(fmt.Sprintf("Dir: %s", rel))
			}
			result.DirsWritten++
			phase.Increment(1)
			continue
		}

		if meta.ContentHash == "" {
			phase.Increment(1)
			continue
		}

		if err := writer.WriteFile(rel, meta, func(out io.Writer) error {
			return rm.writeFileContent(ctx, out, meta)
		}); err != nil {
			if err == errRestoreSkipped {
				phase.Increment(1)
				continue
			}
			phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
			result.Errors++
			phase.Increment(1)
			continue
		}

		if plan.cfg.verbose {
			phase.Log(fmt.Sprintf("File: %s (%d bytes)", rel, meta.Size))
		}
		result.FilesWritten++
		phase.Increment(1)
	}

	phase.Done()
	if err := writer.Close(); err != nil {
		return nil, err
	}
	result.BytesWritten = writer.BytesWritten()
	return result, nil
}

func (rm *RestoreManager) prepareRestore(ctx context.Context, snapshotRef string, opts ...RestoreOption) (restorePlan, error) {
	var cfg restoreConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	rm.metaCache = make(map[string]core.FileMeta)

	snap, resolvedRef, err := rm.resolveSnapshot(ctx, snapshotRef)
	if err != nil {
		return restorePlan{}, err
	}

	byID, err := rm.collectMetadata(snap.Root)
	if err != nil {
		return restorePlan{}, err
	}

	sorted := topoSort(byID)
	if cfg.pathFilter != "" {
		sorted = filterByPath(sorted, byID, cfg.pathFilter)
	}

	return restorePlan{
		cfg:         cfg,
		sorted:      sorted,
		byID:        byID,
		snapshotRef: resolvedRef,
		root:        snap.Root,
	}, nil
}

func secureRestorePath(root, rel string) (string, error) {
	cleanRel := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if cleanRel == "." || cleanRel == "" {
		return filepath.Clean(root), nil
	}
	joined := filepath.Join(root, filepath.FromSlash(cleanRel))
	rootClean := filepath.Clean(root)
	joinedClean := filepath.Clean(joined)
	if joinedClean != rootClean && !strings.HasPrefix(joinedClean, rootClean+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid restore path: %q", rel)
	}
	return joinedClean, nil
}

type zipRestoreWriter struct {
	cw *countingWriter
	zw *zip.Writer
}

func NewZipRestoreWriter(w io.Writer) RestoreWriter {
	cw := &countingWriter{w: w}
	return &zipRestoreWriter{cw: cw, zw: zip.NewWriter(cw)}
}

func (w *zipRestoreWriter) MkdirAll(path string, meta core.FileMeta) error {
	header := &zip.FileHeader{Name: path + "/", Method: zip.Store}
	if meta.Mtime > 0 {
		header.Modified = time.Unix(meta.Mtime, 0)
	}
	_, err := w.zw.CreateHeader(header)
	return err
}

func (w *zipRestoreWriter) WriteFile(path string, meta core.FileMeta, writeContent func(io.Writer) error) error {
	header := &zip.FileHeader{Name: path, Method: zip.Deflate}
	if meta.Mtime > 0 {
		header.Modified = time.Unix(meta.Mtime, 0)
	}
	fw, err := w.zw.CreateHeader(header)
	if err != nil {
		return err
	}
	return writeContent(fw)
}

func (w *zipRestoreWriter) BytesWritten() int64 { return w.cw.count }

func (w *zipRestoreWriter) Close() error {
	if err := w.zw.Close(); err != nil {
		return fmt.Errorf("finalize zip: %w", err)
	}
	return nil
}

type fsRestoreWriter struct {
	root         string
	bytes        int64
	warn         func(string)
	warned       map[string]struct{}
	deferredDirs []deferredRestoreEntry
	deferredFlag []deferredRestoreEntry
}

type deferredRestoreEntry struct {
	path string
	meta core.FileMeta
}

type restoreWarningSetter interface {
	SetWarningFunc(func(string))
}

var errRestoreSkipped = fmt.Errorf("restore entry skipped")

func NewFSRestoreWriter(root string) (RestoreWriter, error) {
	return &fsRestoreWriter{root: root, warned: map[string]struct{}{}}, nil
}

func (w *fsRestoreWriter) SetWarningFunc(fn func(string)) {
	w.warn = fn
}

func (w *fsRestoreWriter) warnf(format string, args ...interface{}) {
	if w.warn != nil {
		w.warn(fmt.Sprintf(format, args...))
	}
}

func (w *fsRestoreWriter) warnOncef(key, format string, args ...interface{}) {
	if _, ok := w.warned[key]; ok {
		return
	}
	w.warned[key] = struct{}{}
	w.warnf(format, args...)
}

func (w *fsRestoreWriter) warnDedupf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	w.warnOncef(msg, "%s", msg)
}

func (w *fsRestoreWriter) MkdirAll(relPath string, meta core.FileMeta) error {
	fullPath, err := secureRestorePath(w.root, relPath)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkComponents(w.root, fullPath); err != nil {
		return err
	}
	st, err := os.Lstat(fullPath)
	if err == nil {
		if !st.IsDir() {
			w.warnf("skipped existing non-directory: %s", relPath)
			return errRestoreSkipped
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(fullPath, 0o755); err != nil {
		return err
	}
	w.deferredDirs = append(w.deferredDirs, deferredRestoreEntry{path: fullPath, meta: meta})
	if meta.Flags != 0 {
		w.deferredFlag = append(w.deferredFlag, deferredRestoreEntry{path: fullPath, meta: meta})
	}
	return nil
}

func (w *fsRestoreWriter) WriteFile(relPath string, meta core.FileMeta, writeContent func(io.Writer) error) error {
	fullPath, err := secureRestorePath(w.root, relPath)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkComponents(w.root, fullPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return err
	}
	if st, err := os.Lstat(fullPath); err == nil {
		if st.IsDir() {
			w.warnf("skipped existing directory collision: %s", relPath)
			return errRestoreSkipped
		}
		w.warnf("skipped existing file: %s", relPath)
		return errRestoreSkipped
	} else if !os.IsNotExist(err) {
		return err
	}

	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	cw := &countingWriter{w: f}
	writeErr := writeContent(cw)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}

	w.bytes += cw.count
	if err := applyRestoreFileMetadata(fullPath, meta, w.warnDedupf); err != nil {
		return err
	}
	if meta.Flags != 0 {
		w.deferredFlag = append(w.deferredFlag, deferredRestoreEntry{path: fullPath, meta: meta})
	}
	return nil
}

func (w *fsRestoreWriter) BytesWritten() int64 { return w.bytes }

func (w *fsRestoreWriter) Close() error {
	for i := len(w.deferredDirs) - 1; i >= 0; i-- {
		entry := w.deferredDirs[i]
		if err := applyRestoreDirMetadata(entry.path, entry.meta, w.warnDedupf); err != nil {
			return err
		}
	}
	for i := len(w.deferredFlag) - 1; i >= 0; i-- {
		entry := w.deferredFlag[i]
		if err := applyRestoreFlags(entry.path, entry.meta, w.warnDedupf); err != nil {
			return err
		}
	}
	return nil
}

func ensureNoSymlinkComponents(root, target string) error {
	rootClean := filepath.Clean(root)
	targetClean := filepath.Clean(target)

	if err := checkSymlinkPath(rootClean); err != nil {
		return err
	}

	if targetClean == rootClean {
		return nil
	}

	rel, err := filepath.Rel(rootClean, targetClean)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	cur := rootClean
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		if err := checkSymlinkPath(cur); err != nil {
			return err
		}
	}
	return nil
}

func checkSymlinkPath(p string) error {
	st, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to restore through symlink path: %s", p)
	}
	return nil
}

func (rm *RestoreManager) dryRunRestore(sorted []core.FileMeta, byID map[string]core.FileMeta, snapshotRef, root string) *RestoreResult {
	result := &RestoreResult{
		SnapshotRef: snapshotRef,
		Root:        root,
		DryRun:      true,
	}
	for _, meta := range sorted {
		if meta.Type == core.FileTypeFolder {
			result.DirsWritten++
		} else if meta.ContentHash != "" {
			result.FilesWritten++
			result.BytesWritten += meta.Size
		}
	}
	return result
}

type countingWriter struct {
	w     io.Writer
	count int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.count += int64(n)
	return n, err
}

func (rm *RestoreManager) writeFileContent(ctx context.Context, w io.Writer, meta core.FileMeta) error {
	contentKey := meta.ContentRef
	if contentKey == "" {
		contentKey = meta.ContentHash
	}

	content, err := rm.loadContent(ctx, contentKey)
	if err != nil {
		return err
	}
	for _, chunkRef := range content.Chunks {
		if err := rm.writeChunk(ctx, w, chunkRef); err != nil {
			return err
		}
	}
	if len(content.DataInlineB64) > 0 {
		if _, err := w.Write(content.DataInlineB64); err != nil {
			return err
		}
	}
	return nil
}

func buildRestorePath(meta core.FileMeta, byID map[string]core.FileMeta) string {
	// Fast path: use stored Paths when available (new snapshots).
	if len(meta.Paths) > 0 {
		return meta.Paths[0]
	}

	// Fallback: reconstruct from parent chain (old snapshots).
	const maxDepth = 50
	parts := []string{meta.Name}
	cur := meta
	for i := 0; i < maxDepth && len(cur.Parents) > 0; i++ {
		parent, ok := byID[cur.Parents[0]]
		if !ok {
			break
		}
		parts = append(parts, parent.Name)
		cur = parent
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return path.Join(parts...)
}

// filterByPath returns only the entries whose restore path matches the given filter.
// If the filter ends with "/", it matches all entries under that subtree.
// Otherwise it matches only the entry with the exact path.
// Ancestor directories of matched entries are always included.
func filterByPath(sorted []core.FileMeta, byID map[string]core.FileMeta, pathFilter string) []core.FileMeta {
	isSubtree := strings.HasSuffix(pathFilter, "/")
	prefix := pathFilter
	if isSubtree {
		prefix = strings.TrimSuffix(pathFilter, "/")
	}

	// Build a set of restore paths for each entry.
	restorePaths := make(map[string]string, len(sorted))
	for _, meta := range sorted {
		restorePaths[meta.FileID] = buildRestorePath(meta, byID)
	}

	// Determine which entries are matched.
	matched := make(map[string]bool)
	for _, meta := range sorted {
		p := restorePaths[meta.FileID]
		if isSubtree {
			// Match the directory itself and anything under it.
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				matched[meta.FileID] = true
			}
		} else {
			// Exact match, or — when the target is a folder — include
			// everything under it so the user doesn't need a trailing "/".
			if p == pathFilter || strings.HasPrefix(p, pathFilter+"/") {
				matched[meta.FileID] = true
			}
		}
	}

	// Include all ancestor directories of matched entries by walking
	// up the full parent chain (not just immediate parents).
	var walkAncestors func(id string)
	walkAncestors = func(id string) {
		meta, ok := byID[id]
		if !ok {
			return
		}
		for _, parentID := range meta.Parents {
			if matched[parentID] {
				continue
			}
			if _, ok := byID[parentID]; ok {
				matched[parentID] = true
				walkAncestors(parentID)
			}
		}
	}
	for _, meta := range sorted {
		if matched[meta.FileID] {
			walkAncestors(meta.FileID)
		}
	}

	var filtered []core.FileMeta
	for _, meta := range sorted {
		if matched[meta.FileID] {
			filtered = append(filtered, meta)
		}
	}
	return filtered
}

// ---------------------------------------------------------------------------
// Snapshot resolution
// ---------------------------------------------------------------------------

func (rm *RestoreManager) resolveSnapshot(ctx context.Context, ref string) (*core.Snapshot, string, error) {
	if ref == "" || ref == "latest" {
		data, err := rm.store.Get(ctx, "index/latest")
		if err != nil {
			return nil, "", fmt.Errorf("cannot find latest index: %w", err)
		}
		var idx core.Index
		if err := json.Unmarshal(data, &idx); err != nil {
			return nil, "", err
		}
		ref = idx.LatestSnapshot
	} else if !strings.HasPrefix(ref, "snapshot/") {
		ref = "snapshot/" + ref
	}

	data, err := rm.store.Get(ctx, ref)
	if err != nil {
		return nil, "", fmt.Errorf("load snapshot %s: %w", ref, err)
	}
	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, "", err
	}
	return &snap, ref, nil
}

// ---------------------------------------------------------------------------
// Metadata collection
// ---------------------------------------------------------------------------

func (rm *RestoreManager) collectMetadata(root string) (map[string]core.FileMeta, error) {
	var refs []string
	err := rm.tree.Walk(root, func(_, valueRef string) error {
		refs = append(refs, valueRef)
		return nil
	})
	if err != nil {
		return nil, err
	}

	phase := rm.reporter.StartPhase("Loading metadata", int64(len(refs)), false)

	byID := make(map[string]core.FileMeta, len(refs))
	for _, ref := range refs {
		fm, err := rm.loadMeta(ref)
		if err != nil {
			phase.Error()
			return nil, err
		}
		byID[fm.FileID] = *fm
		phase.Increment(1)
	}
	phase.Done()
	return byID, nil
}

func (rm *RestoreManager) loadMeta(ref string) (*core.FileMeta, error) {
	if fm, ok := rm.metaCache[ref]; ok {
		return &fm, nil
	}
	data, err := rm.store.Get(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	var fm core.FileMeta
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil, err
	}
	return &fm, nil
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

// topoSort returns entries in parent-before-child order so that directories
// are created before the files they contain. Parents contain FileIDs.
func topoSort(byID map[string]core.FileMeta) []core.FileMeta {
	var out []core.FileMeta
	visited := make(map[string]bool, len(byID))

	var visit func(core.FileMeta)
	visit = func(meta core.FileMeta) {
		if visited[meta.FileID] {
			return
		}
		for _, parentID := range meta.Parents {
			if parent, ok := byID[parentID]; ok {
				visit(parent)
			}
		}
		visited[meta.FileID] = true
		out = append(out, meta)
	}

	for _, meta := range byID {
		visit(meta)
	}
	return out
}

// ---------------------------------------------------------------------------
// Content loading
// ---------------------------------------------------------------------------

func (rm *RestoreManager) loadContent(ctx context.Context, hash string) (*core.Content, error) {
	data, err := rm.store.Get(ctx, "content/"+hash)
	if err != nil {
		return nil, err
	}
	var c core.Content
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (rm *RestoreManager) writeChunk(ctx context.Context, w io.Writer, ref string) error {
	data, err := rm.store.Get(ctx, ref)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
