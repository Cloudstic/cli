package engine

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

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
	noVerify   bool
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

// WithRestoreNoVerify skips the content-hash check that restore normally runs
// over every file it writes. Verification is on by default; this exists as an
// escape hatch for the case where a snapshot records a hash that disagrees with
// its own content, so that the data can still be recovered.
func WithRestoreNoVerify() RestoreOption {
	return func(cfg *restoreConfig) { cfg.noVerify = true }
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

// concurrentRestoreWriter is the optional opt-in a RestoreWriter uses to
// declare that WriteFile is safe to call from several goroutines at once.
// Without it runWithWriter restores files one at a time.
//
// It is an opt-in rather than the default because it is not universally
// achievable: a zip archive is a single sequential stream with one open entry
// at a time, so zipRestoreWriter deliberately does not implement it. A
// directory tree has no such constraint — distinct files are distinct fds —
// so fsRestoreWriter does.
type concurrentRestoreWriter interface {
	SupportsConcurrentWrites() bool
}

// restoreMemoryBudget caps the total bytes of chunk data that have been
// fetched but not yet written, across every file being restored at once.
// Restore fans out on two axes now — several files in flight, several chunks
// per file — so neither axis can be the memory bound on its own; this is.
// It mirrors the equivalent cap on the backup side (see backup_upload.go).
const restoreMemoryBudget = 128 * 1024 * 1024

// RestoreManager recreates a snapshot's file tree using a RestoreWriter output format.
type RestoreManager struct {
	store     store.ObjectStore
	tree      *hamt.Tree
	reporter  ui.Reporter
	memBudget *semaphore.Weighted
}

func NewRestoreManager(s store.ObjectStore, reporter ui.Reporter) *RestoreManager {
	return &RestoreManager{
		store:     s,
		tree:      hamt.NewTree(s),
		reporter:  reporter,
		memBudget: semaphore.NewWeighted(restoreMemoryBudget),
	}
}

// Run restores the snapshot's file tree to the provided writer format.
// snapshotRef can be "", "latest", a bare hash, or "snapshot/<hash>".
func (rm *RestoreManager) Run(ctx context.Context, writer RestoreWriter, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	lock, lockedCtx, err := AcquireSharedLock(ctx, rm.store, "restore")
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	ctx = lockedCtx

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

// runWithWriter walks the topologically sorted entries, creating directories
// and writing file contents.
//
// Directories are handled inline and in order: plan.sorted lists a parent
// before its children, so creating them on this goroutine is what guarantees
// a file's directory exists by the time the file is dispatched.
//
// File contents are the expensive part — each one is at least a content-object
// fetch, and for a large file a whole sequence of chunk fetches. Restoring
// them one after another means exactly one request is ever outstanding, so on
// a high-latency backend the wall time collapses to (file count x round trip)
// no matter how much bandwidth or store concurrency is available. They are
// therefore dispatched to a bounded worker pool whenever the writer says it
// can take concurrent writes; the pool degenerates to sequential execution for
// writers that cannot (zip).
func (rm *RestoreManager) runWithWriter(ctx context.Context, plan restorePlan, writer RestoreWriter) (*RestoreResult, error) {
	result := &RestoreResult{SnapshotRef: plan.snapshotRef, Root: plan.root}
	phase := rm.reporter.StartPhase("Restoring", int64(len(plan.sorted)), false)

	// Counters are touched from the file workers as well as this goroutine.
	var mu sync.Mutex
	bump := func(field *int) {
		mu.Lock()
		*field++
		mu.Unlock()
	}

	if setter, ok := writer.(restoreWarningSetter); ok {
		setter.SetWarningFunc(func(msg string) {
			bump(&result.Warnings)
			phase.Log("Warning: " + msg)
		})
	}

	cw, ok := writer.(concurrentRestoreWriter)
	concurrent := ok && cw.SupportsConcurrentWrites()

	g, gCtx := errgroup.WithContext(ctx)
	if concurrent {
		g.SetLimit(restoreFileConcurrency(rm.store))
	}

	// A failure to write one file is reported and counted, not fatal — the
	// rest of the snapshot is still worth recovering. Only a context
	// cancellation stops the walk, which is why the worker returns nil here
	// and errgroup carries just gCtx's error.
	restoreFile := func(meta core.FileMeta, rel string) func() error {
		return func() error {
			err := writer.WriteFile(rel, meta, func(out io.Writer) error {
				return rm.writeFileContent(gCtx, out, meta, !plan.cfg.noVerify)
			})
			defer phase.Increment(1)
			switch {
			case errors.Is(err, errRestoreSkipped):
				return nil
			case err != nil:
				phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
				bump(&result.Errors)
				return nil
			}
			if plan.cfg.verbose {
				phase.Log(fmt.Sprintf("File: %s (%d bytes)", rel, meta.Size))
			}
			bump(&result.FilesWritten)
			return nil
		}
	}

	for _, meta := range plan.sorted {
		rel := buildRestorePath(meta, plan.byID)

		if meta.Type == core.FileTypeFolder {
			if err := writer.MkdirAll(rel, meta); err != nil {
				if !errors.Is(err, errRestoreSkipped) {
					phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
					result.Errors++
				}
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

		// A sequential writer runs inline, not through the pool. Even a pool of
		// one would let a file write overlap the MkdirAll of a later entry on
		// this goroutine, and for a zip that means a directory header written
		// into the middle of an open file entry.
		if !concurrent {
			_ = restoreFile(meta, rel)()
			continue
		}
		g.Go(restoreFile(meta, rel))
	}

	if err := g.Wait(); err != nil {
		phase.Error()
		return nil, err
	}

	phase.Done()
	if err := writer.Close(); err != nil {
		return nil, err
	}
	result.BytesWritten = writer.BytesWritten()
	return result, nil
}

// restoreFileConcurrency picks how many files to reconstruct at once.
//
// It is deliberately far below the store's own concurrency hint (128 for S3):
// each file in flight opens its own chunk window underneath it, so the two
// levels multiply. Memory is bounded by restoreMemoryBudget regardless, but
// dividing the budget across too many files would starve each one's window
// and lose the pipelining this exists to enable. A modest fan-out is enough
// to hide per-file round trips, which is the actual bottleneck for the many
// small-to-medium files that dominate a real snapshot.
func restoreFileConcurrency(s store.ObjectStore) int {
	return min(store.GetConcurrencyHint(s, 8), 16)
}

func (rm *RestoreManager) prepareRestore(ctx context.Context, snapshotRef string, opts ...RestoreOption) (restorePlan, error) {
	var cfg restoreConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	snap, resolvedRef, err := rm.resolveSnapshot(ctx, snapshotRef)
	if err != nil {
		return restorePlan{}, err
	}

	byID, err := rm.collectMetadata(ctx, snap.Root)
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
	root string

	// mu guards every field below it. WriteFile runs concurrently (see
	// SupportsConcurrentWrites), so the counters, the warn-dedup set, and the
	// deferred-metadata lists are all shared mutable state. The lock is only
	// ever held around bookkeeping — never across the content write itself,
	// which is the whole point of restoring files in parallel.
	mu           sync.Mutex
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

// SupportsConcurrentWrites implements concurrentRestoreWriter. Two WriteFile
// calls for distinct paths touch disjoint files; the shared bookkeeping they
// do touch is guarded by w.mu.
func (w *fsRestoreWriter) SupportsConcurrentWrites() bool { return true }

func (w *fsRestoreWriter) SetWarningFunc(fn func(string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warn = fn
}

func (w *fsRestoreWriter) warnf(format string, args ...interface{}) {
	w.mu.Lock()
	fn := w.warn
	w.mu.Unlock()
	if fn != nil {
		fn(fmt.Sprintf(format, args...))
	}
}

func (w *fsRestoreWriter) warnOncef(key, format string, args ...interface{}) {
	w.mu.Lock()
	if _, ok := w.warned[key]; ok {
		w.mu.Unlock()
		return
	}
	w.warned[key] = struct{}{}
	w.mu.Unlock()
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
	w.mu.Lock()
	w.deferredDirs = append(w.deferredDirs, deferredRestoreEntry{path: fullPath, meta: meta})
	if meta.Flags != 0 {
		w.deferredFlag = append(w.deferredFlag, deferredRestoreEntry{path: fullPath, meta: meta})
	}
	w.mu.Unlock()
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
		// Never leave known-bad bytes behind under the user's own filename.
		// This covers a failed content-hash check as well as a stream that
		// died partway, which previously left a truncated file on disk.
		_ = os.Remove(fullPath)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(fullPath)
		return closeErr
	}

	w.mu.Lock()
	w.bytes += cw.count
	w.mu.Unlock()

	if err := applyRestoreFileMetadata(fullPath, meta, w.warnDedupf); err != nil {
		return err
	}
	if meta.Flags != 0 {
		w.mu.Lock()
		w.deferredFlag = append(w.deferredFlag, deferredRestoreEntry{path: fullPath, meta: meta})
		w.mu.Unlock()
	}
	return nil
}

func (w *fsRestoreWriter) BytesWritten() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes
}

// Close runs after every WriteFile has returned, so it needs no lock of its
// own — but it must stay that way: the deferred lists are ordered, and
// applying directory metadata is what makes parent mtimes survive the writes
// underneath them.
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

func (rm *RestoreManager) writeFileContent(ctx context.Context, w io.Writer, meta core.FileMeta, verify bool) error {
	contentKey := meta.ContentRef
	if contentKey == "" {
		contentKey = meta.ContentHash
	}

	content, err := rm.loadContent(ctx, contentKey)
	if err != nil {
		return err
	}

	// Hash the reconstructed stream as it is written. meta.ContentHash is a
	// SHA-256 over the whole plaintext for both storage paths — the chunked
	// path accumulates it in Chunker.ProcessStream, the inline path computes it
	// directly — so one digest of the output is directly comparable. Nothing
	// below this line authenticates the bytes otherwise: an object replaced in
	// the backing store is returned by the store stack without complaint.
	hasher := sha256.New()
	out := w
	if verify {
		out = io.MultiWriter(w, hasher)
	}

	if err := rm.writeChunks(ctx, out, content.Chunks, avgChunkSize(content)); err != nil {
		return err
	}
	if len(content.DataInlineB64) > 0 {
		if _, err := out.Write(content.DataInlineB64); err != nil {
			return err
		}
	}

	if verify {
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != meta.ContentHash {
			return fmt.Errorf("content hash mismatch: expected %s, got %s", meta.ContentHash, got)
		}
	}
	return nil
}

func buildRestorePath(meta core.FileMeta, byID map[string]core.FileMeta) string {
	return fileMetaPath(meta, func(parentID string) (core.FileMeta, bool) {
		parent, ok := byID[parentID]
		return parent, ok
	})
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
	ref, err := resolveSnapshotRef(ctx, rm.store, ref)
	if err != nil {
		return nil, "", err
	}

	snap, err := loadSnapshotByRef(ctx, rm.store, ref)
	if err != nil {
		return nil, "", err
	}
	return snap, ref, nil
}

// ---------------------------------------------------------------------------
// Metadata collection
// ---------------------------------------------------------------------------

func (rm *RestoreManager) collectMetadata(ctx context.Context, root string) (map[string]core.FileMeta, error) {
	var refs []string
	err := rm.tree.Walk(ctx, root, func(_, valueRef string) error {
		refs = append(refs, valueRef)
		return nil
	})
	if err != nil {
		return nil, err
	}

	phase := rm.reporter.StartPhase("Loading metadata", int64(len(refs)), false)

	// Fetch concurrently (mirroring fetchSnapshots' pattern): each ref is a
	// small, independent JSON object with no ordering requirement among
	// them, unlike a file's content chunks, so results can land in the map
	// as soon as they arrive rather than needing to be sequenced.
	byID := make(map[string]core.FileMeta, len(refs))
	var mu sync.Mutex

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(store.GetConcurrencyHint(rm.store, 10))
	for _, ref := range refs {
		g.Go(func() error {
			fm, err := rm.loadMeta(gCtx, ref)
			if err != nil {
				return err
			}
			mu.Lock()
			byID[fm.FileID] = *fm
			mu.Unlock()
			phase.Increment(1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		phase.Error()
		return nil, err
	}
	phase.Done()
	return byID, nil
}

func (rm *RestoreManager) loadMeta(ctx context.Context, ref string) (*core.FileMeta, error) {
	data, err := getVerified(ctx, rm.store, ref)
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

// avgChunkSize estimates a Content's per-chunk payload so writeChunks can
// charge the memory budget without having to fetch a chunk to find out how
// big one is.
func avgChunkSize(c *core.Content) int64 {
	if c == nil || len(c.Chunks) == 0 || c.Size <= 0 {
		return 0
	}
	return c.Size / int64(len(c.Chunks))
}

func (rm *RestoreManager) writeChunk(ctx context.Context, w io.Writer, ref string) error {
	data, err := rm.store.Get(ctx, ref)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// writeChunks fetches a file's content chunks and writes them to w in order.
//
// The store's Get is the bottleneck for a large file, so fetches run
// concurrently. They run as a *sliding window* rather than as discrete
// batches: a fixed number of fetches stay in flight at all times, and each
// chunk is written the moment the chunk before it has been written. A batched
// version — fetch N, wait for all N, write all N — leaves the network
// completely idle for the whole write phase of every batch and stalls the
// entire batch behind its single slowest Get, which on a high-latency backend
// such as S3 roughly halves throughput.
//
// Chunks still land on w in their original order regardless of which fetch
// finishes first, since the reconstructed stream must be byte-identical to
// what was chunked at backup time.
//
// avgChunkSize is the caller's estimate of the per-chunk payload, used to
// charge the shared memory budget (see RestoreManager.memBudget). Fetches
// block once the budget is exhausted, which is what bounds resident memory
// now that the window is no longer a hard chunk count.
func (rm *RestoreManager) writeChunks(ctx context.Context, w io.Writer, refs []string, avgChunkSize int64) error {
	if len(refs) <= 1 {
		for _, ref := range refs {
			if err := rm.writeChunk(ctx, w, ref); err != nil {
				return err
			}
		}
		return nil
	}

	window := min(store.GetConcurrencyHint(rm.store, 10), len(refs))
	weight := chunkWeight(avgChunkSize)

	type slot struct {
		data  []byte
		err   error
		ready chan struct{}
	}
	slots := make([]slot, window)
	for i := range slots {
		slots[i] = slot{ready: make(chan struct{})}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// fetch runs refs[i] into the slot it will be consumed from. The slot is
	// reused every `window` chunks, so it is re-armed by the consumer before
	// the next fetch targeting it is started — never concurrently with it.
	fetch := func(i int) {
		s := &slots[i%window]
		if err := rm.memBudget.Acquire(ctx, weight); err != nil {
			s.err = err
			close(s.ready)
			return
		}
		d, err := rm.store.Get(ctx, refs[i])
		if err != nil {
			rm.memBudget.Release(weight)
			s.err = fmt.Errorf("fetch chunk %s: %w", refs[i], err)
			close(s.ready)
			return
		}
		s.data = d
		close(s.ready)
	}

	var wg sync.WaitGroup
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fetch(i)
		}()
	}

	for i := 0; i < window; i++ {
		start(i)
	}

	// Draining every outstanding fetch before returning keeps the memory
	// budget balanced on the error path: an abandoned fetch that later
	// completes would otherwise hold its reservation forever.
	drain := func() {
		cancel()
		wg.Wait()
		for i := range slots {
			select {
			case <-slots[i].ready:
				if slots[i].err == nil && slots[i].data != nil {
					rm.memBudget.Release(weight)
				}
			default:
			}
		}
	}

	for i := 0; i < len(refs); i++ {
		s := &slots[i%window]
		<-s.ready
		if s.err != nil {
			err := s.err
			s.data, s.err = nil, nil // claimed; drain must not double-release
			drain()
			return err
		}

		data := s.data
		_, werr := w.Write(data)
		s.data = nil
		rm.memBudget.Release(weight)
		if werr != nil {
			drain()
			return werr
		}

		// Re-arm this slot and pull the next chunk that will land in it.
		if next := i + window; next < len(refs) {
			s.ready = make(chan struct{})
			start(next)
		}
	}

	wg.Wait()
	return nil
}

// chunkWeight turns a per-chunk size estimate into the amount charged against
// the restore memory budget. The estimate comes from a Content object's own
// size and chunk count, so it is close for the common case; the clamp keeps a
// bogus or missing estimate from either serialising the window (too large) or
// letting it grow without bound (too small).
func chunkWeight(avgChunkSize int64) int64 {
	const minWeight = 64 * 1024
	if avgChunkSize <= 0 {
		return cdcMaxSize
	}
	return max(minWeight, min(avgChunkSize, cdcMaxSize))
}
