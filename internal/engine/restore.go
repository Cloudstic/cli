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
	pathFilter string
	noVerify   bool
}

// WithRestoreDryRun resolves the snapshot and reports what would be restored without writing output.
func WithRestoreDryRun() RestoreOption {
	return func(cfg *restoreConfig) { cfg.dryRun = true }
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
	metas     *metaLoader
	reporter  ui.Reporter
	memBudget *semaphore.Weighted
}

func NewRestoreManager(d Deps) *RestoreManager {
	return &RestoreManager{
		store:     d.Store,
		tree:      hamt.NewTree(d.Store),
		metas:     newUncachedMetaLoader(d.Store),
		reporter:  d.Reporter,
		memBudget: semaphore.NewWeighted(restoreMemoryBudget),
	}
}

// Run restores the snapshot's file tree to the provided writer format.
// snapshotRef can be "", "latest", a bare hash, or "snapshot/<hash>".
//
// The traversal is derived from the tree rather than planned in memory: see
// restore_derived.go. What remains here is the completeness check that makes
// derivation safe to rely on — the tree's own entry count against what the walk
// claimed — and the materialised fallback for a snapshot it cannot enumerate.
func (rm *RestoreManager) Run(ctx context.Context, writer RestoreWriter, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	lock, lockedCtx, err := AcquireSharedLock(ctx, rm.store, "restore")
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	ctx = lockedCtx

	var cfg restoreConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if writer == nil && !cfg.dryRun {
		return nil, fmt.Errorf("restore writer is required")
	}

	snap, resolvedRef, err := rm.resolveSnapshot(ctx, snapshotRef)
	if err != nil {
		return nil, err
	}

	total, err := rm.countEntries(ctx, snap.Root)
	if err != nil {
		return nil, err
	}

	result := &RestoreResult{SnapshotRef: resolvedRef, Root: snap.Root, DryRun: cfg.dryRun}
	out := rm.newEmitter(ctx, writer, cfg, result, total)

	walk := &derivedWalk{
		tree:   rm.tree,
		store:  rm.store,
		metas:  rm.metas,
		filter: newRestorePathFilter(cfg.pathFilter),
		out:    out,
	}
	reached, err := walk.run(ctx, snap.Root)
	if err != nil {
		out.fail()
		return nil, err
	}

	if reached < total {
		if err := rm.restoreUnreached(ctx, snap.Root, cfg, out); err != nil {
			out.fail()
			return nil, err
		}
	}

	if err := out.finish(); err != nil {
		return nil, err
	}
	return result, nil
}

// restoreEmitter turns entries into output, whichever traversal found them.
//
// Directories are created inline on the traversal's own goroutine, which is what
// guarantees a file's directory exists by the time the file is dispatched. File
// contents are the expensive part — each one is at least a content-object fetch,
// and for a large file a whole sequence of chunk fetches. Restoring them one
// after another means exactly one request is ever outstanding, so on a
// high-latency backend the wall time collapses to (file count x round trip) no
// matter how much bandwidth or store concurrency is available. They are
// therefore dispatched to a bounded worker pool whenever the writer says it can
// take concurrent writes; the pool degenerates to sequential execution for
// writers that cannot (zip).
type restoreEmitter struct {
	rm     *RestoreManager
	writer RestoreWriter
	cfg    restoreConfig
	result *RestoreResult
	phase  ui.Phase

	// mu guards result's counters, which the file workers touch as well as the
	// traversal goroutine.
	mu sync.Mutex

	g          *errgroup.Group
	gctx       context.Context
	concurrent bool
}

func (rm *RestoreManager) newEmitter(ctx context.Context, writer RestoreWriter, cfg restoreConfig, result *RestoreResult, total int64) *restoreEmitter {
	e := &restoreEmitter{
		rm:     rm,
		writer: writer,
		cfg:    cfg,
		result: result,
		phase:  rm.reporter.StartPhase("Restoring", total, false),
	}
	if cfg.dryRun {
		return e
	}

	if setter, ok := writer.(restoreWarningSetter); ok {
		setter.SetWarningFunc(func(msg string) {
			e.bump(&result.Warnings)
			e.phase.Log("Warning: " + msg)
		})
	}
	cw, ok := writer.(concurrentRestoreWriter)
	e.concurrent = ok && cw.SupportsConcurrentWrites()
	e.g, e.gctx = errgroup.WithContext(ctx)
	if e.concurrent {
		e.g.SetLimit(restoreFileConcurrency(rm.store))
	}
	return e
}

func (e *restoreEmitter) bump(field *int) {
	e.mu.Lock()
	*field++
	e.mu.Unlock()
}

// skip accounts for an entry the traversal reached and the path filter dropped.
// Progress is measured against the snapshot's entry count, so an entry that is
// not written still has to be counted off.
func (e *restoreEmitter) skip() { e.phase.Increment(1) }

// ensureDir creates a directory and every uncreated ancestor above it.
//
// Deferring is what a path filter needs: a directory merely on the way to the
// selection is created only once something under it survives, which is what
// filterByPath expressed as a second pass over the materialised plan.
func (e *restoreEmitter) ensureDir(d *restoreDir) {
	if d == nil || d.made {
		return
	}
	e.ensureDir(d.parent)
	d.made = true
	meta := *d.meta
	d.meta = nil // released as soon as it has been used; see restoreDir
	// A deferred directory was already counted off when the filter passed over
	// it; creating it later must not count it twice.
	e.emitDir(meta, d.rel, !d.charged)
}

// dir creates one directory the traversal reached for the first time.
func (e *restoreEmitter) dir(meta core.FileMeta, rel string) {
	e.emitDir(meta, rel, true)
}

// emitDir creates one directory. A failure is counted and reported, not fatal:
// the rest of the snapshot is still worth recovering.
func (e *restoreEmitter) emitDir(meta core.FileMeta, rel string, charge bool) {
	if charge {
		defer e.phase.Increment(1)
	}
	if e.cfg.dryRun {
		e.result.DirsWritten++
		return
	}
	if err := e.writer.MkdirAll(rel, meta); err != nil {
		if !errors.Is(err, errRestoreSkipped) {
			e.phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
			// Through bump, not directly: a file worker counts its own failures
			// into the same field while this goroutine is creating directories.
			e.bump(&e.result.Errors)
		}
		return
	}
	e.phase.Logf(ui.DetailVerbose, "Dir: %s", rel)
	e.result.DirsWritten++
}

// file writes one file, on the worker pool where the writer allows it.
func (e *restoreEmitter) file(meta core.FileMeta, rel string) {
	if meta.ContentHash == "" {
		e.phase.Increment(1)
		return
	}
	if e.cfg.dryRun {
		e.result.FilesWritten++
		e.result.BytesWritten += meta.Size
		e.phase.Increment(1)
		return
	}

	// A failure to write one file is reported and counted, not fatal. Only a
	// context cancellation stops the traversal, which is why the worker returns
	// nil here and errgroup carries just gctx's error.
	work := func() error {
		err := e.writer.WriteFile(rel, meta, func(out io.Writer) error {
			return e.rm.writeFileContent(e.gctx, out, meta, !e.cfg.noVerify)
		})
		defer e.phase.Increment(1)
		switch {
		case errors.Is(err, errRestoreSkipped):
			return nil
		case err != nil:
			e.phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
			e.bump(&e.result.Errors)
			return nil
		}
		e.phase.Logf(ui.DetailVerbose, "File: %s (%d bytes)", rel, meta.Size)
		e.bump(&e.result.FilesWritten)
		return nil
	}

	// A sequential writer runs inline, not through the pool. Even a pool of one
	// would let a file write overlap the MkdirAll of a later entry on the
	// traversal goroutine, and for a zip that means a directory header written
	// into the middle of an open file entry.
	if !e.concurrent {
		_ = work()
		return
	}
	e.g.Go(work)
}

// fail reports the phase as failed and drains outstanding writes.
func (e *restoreEmitter) fail() {
	if e.g != nil {
		_ = e.g.Wait()
	}
	e.phase.Error()
}

// finish waits for outstanding writes and closes the writer.
func (e *restoreEmitter) finish() error {
	if e.cfg.dryRun {
		e.phase.Done()
		return nil
	}
	if err := e.g.Wait(); err != nil {
		e.phase.Error()
		return err
	}
	e.phase.Done()
	if err := e.writer.Close(); err != nil {
		return err
	}
	e.result.BytesWritten = e.writer.BytesWritten()
	return nil
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

// countEntries is how many entries the snapshot's tree holds.
//
// It reads nodes and no filemeta, so it costs a fraction of what the entries
// themselves do, and it is the only thing that makes derivation safe to rely on:
// derived order enumerates by primary parent, and this is the number the walk's
// own count is checked against before the restore is called complete.
func (rm *RestoreManager) countEntries(ctx context.Context, root string) (int64, error) {
	var n int64
	err := rm.tree.Walk(ctx, root, func(string, string) error {
		n++
		return nil
	})
	return n, err
}

// restoreUnreached restores whatever the derived walk could not enumerate.
//
// Three kinds of entry land here, and all three are why restoreOrder's fallback
// passes were written: an entry whose primary parent is missing from the
// snapshot, one whose primary-parent chain is a cycle, and one routed by a build
// predating affinity keys — for which the derivation's descent looks in a
// subtree the entry is not in. This is the O(files) plan the ordinary path
// exists to avoid, and it is built only when the count says something was
// missed.
//
// One narrow difference from restoring everything this way: under -path, a
// directory the derivation reached but deferred creating (nothing under it
// matched) stays uncreated even if a *complement* entry below it now matches. No
// file is lost — fsRestoreWriter creates a missing parent on the way to the file
// — but that directory's own recorded mode and mtime are not applied. It needs a
// snapshot that is both malformed and being partially restored to reach.
func (rm *RestoreManager) restoreUnreached(ctx context.Context, root string, cfg restoreConfig, out *restoreEmitter) error {
	byID, walkOrder, routing, err := rm.collectMetadata(ctx, root)
	if err != nil {
		return err
	}

	sorted := restoreOrder(byID, walkOrder)
	if cfg.pathFilter != "" {
		sorted = filterByPath(sorted, byID, cfg.pathFilter)
	}
	reached := derivedReachable(byID, routing)
	remaining := sorted[:0]
	for _, id := range sorted {
		if !reached[id] {
			remaining = append(remaining, id)
		}
	}

	// This phase declares nothing either, for the reason the derived walk gives
	// (restore_derived.go) and #500 measured: admission consumes a declaration as
	// a claim about when the objects will be read, and a write phase fanning out
	// across restoreFileConcurrency files touches every pack before returning to
	// any of them.
	for _, id := range remaining {
		meta := byID[id]
		rel := buildRestorePath(meta, byID)
		if meta.Type == core.FileTypeFolder {
			out.dir(meta, rel)
			continue
		}
		out.file(meta, rel)
	}
	return nil
}

// derivedReachable replays the derived walk over a materialised snapshot,
// reporting exactly which entries it would have claimed.
//
// It is a replay rather than an equivalent rule, and the difference matters. The
// obvious analytic test — "the primary-parent chain reaches the root through
// folders" — says yes for every entry of a pre-affinity repository, whose
// entries the descent cannot find at all. Believing it would silently restore
// nothing. Comparing each entry's stored routing prefix against the one its
// primary parent implies is what the descent actually does.
func derivedReachable(byID map[string]core.FileMeta, routing map[string]string) map[string]bool {
	children := make(map[string][]string, len(byID))
	for id, meta := range byID {
		parent := primaryParentID(&meta)
		children[parent] = append(children[parent], id)
	}

	reached := make(map[string]bool, len(byID))
	var descend func(dirID string)
	descend = func(dirID string) {
		want := childRoutingPrefix(dirID)
		for _, id := range children[dirID] {
			if reached[id] || routing[id] != want {
				continue
			}
			reached[id] = true
			if byID[id].Type == core.FileTypeFolder {
				descend(id)
			}
		}
	}
	descend("")
	return reached
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
func filterByPath(sorted []string, byID map[string]core.FileMeta, pathFilter string) []string {
	isSubtree := strings.HasSuffix(pathFilter, "/")
	prefix := pathFilter
	if isSubtree {
		prefix = strings.TrimSuffix(pathFilter, "/")
	}

	// Determine which entries are matched. The path is built once per entry and
	// tested immediately rather than being kept in a map for a second pass: the
	// only thing the second pass needed was the path, and nothing after this
	// reads it.
	matched := make(map[string]bool)
	for _, id := range sorted {
		p := buildRestorePath(byID[id], byID)
		if isSubtree {
			// Match the directory itself and anything under it.
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				matched[id] = true
			}
		} else {
			// Exact match, or — when the target is a folder — include
			// everything under it so the user doesn't need a trailing "/".
			if p == pathFilter || strings.HasPrefix(p, pathFilter+"/") {
				matched[id] = true
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
	for _, id := range sorted {
		if matched[id] {
			walkAncestors(id)
		}
	}

	var filtered []string
	for _, id := range sorted {
		if matched[id] {
			filtered = append(filtered, id)
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

// collectMetadata loads every entry in the snapshot, and reports the order the
// tree walk yielded them in and the routing prefix each was stored under as well
// as the entries themselves.
//
// **This is the O(files) plan, and the ordinary restore no longer builds one.**
// Only restoreUnreached calls it, for a snapshot whose entries the derived walk
// could not enumerate — so the cost of holding a whole snapshot's metadata is
// now paid by malformed and pre-affinity repositories rather than by every
// restore.
//
// The order matters to what comes after, and the two orders are different.
// Restore *writes* in walk order, because that is the order backup laid entries
// out and writing in any other scatters reads across every pack — measured at
// 55% pack-cache misses against 0.6% (RFC 0023). Restore *fetches* in whatever
// order the store says is cheapest, which on a repository built by several
// backups is not walk order at all: each backup's entries sit in its own packs,
// so the walk interleaves them. See the grouping below.
func (rm *RestoreManager) collectMetadata(ctx context.Context, root string) (map[string]core.FileMeta, []string, map[string]string, error) {
	var (
		refs     []string
		prefixes []string
	)
	err := rm.tree.WalkRouted(ctx, root, func(routingKey, _, valueRef string) error {
		refs = append(refs, valueRef)
		// A key too short to carry a prefix records none, which matches no
		// directory and so leaves the entry to this traversal rather than to
		// the derived one — the safe direction.
		//
		// Cloned rather than sliced: a routing key is a field of a cached node,
		// and holding a four-character view of it would pin the whole node.
		prefix := ""
		if len(routingKey) >= derivedPrefixLen {
			prefix = strings.Clone(routingKey[:derivedPrefixLen])
		}
		prefixes = append(prefixes, prefix)
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}

	phase := rm.reporter.StartPhase("Loading metadata", int64(len(refs)), false)

	// Fetch concurrently (mirroring fetchSnapshots' pattern): each ref is a
	// small, independent JSON object with no ordering requirement among
	// them, unlike a file's content chunks, so results can land in the map
	// as soon as they arrive rather than needing to be sequenced.
	//
	// Walk order is kept separately, by writing each result to its own index
	// rather than appending as it lands: concurrency decides when a result
	// arrives, and that must not decide the order the restore writes in.
	byID := make(map[string]core.FileMeta, len(refs))
	walkOrder := make([]string, len(refs))
	routing := make(map[string]string, len(refs))
	var mu sync.Mutex

	// Fetch in the groups the store says are cheapest, which is not the order
	// the walk produced. Walk order matches the layout of whichever backup wrote
	// each entry, so on a repository built by several backups it interleaves
	// packs — and a pack read in pieces spread across the fetch is transferred,
	// evicted and transferred again. Handing the whole set over first lets
	// PackStore group it, because it is the layer that knows where objects live
	// and this is the one moment the full set is known in advance.
	//
	// **The unit of concurrency is the group, not the ref**, and that is what
	// makes grouping worth anything. Spreading every ref across the errgroup
	// interleaves the packs again as soon as more than one worker runs, which is
	// the arrangement grouping exists to prevent; one worker per group reads a
	// pack's objects consecutively while other workers read other packs. The
	// concurrency hint therefore bounds how many pack bodies are live at once.
	//
	// walkOrder is unaffected: it is filled by index below, so the order results
	// are *fetched* in is independent of the order restore *writes* in, which
	// must stay walk order (RFC 0023 §5).
	plan := store.PlanReads(ctx, rm.store, refs)
	groups := indexGroups(plan.Groups, refs)

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(plan.Concurrency)
	for _, group := range groups {
		g.Go(func() error {
			for _, f := range group {
				fm, err := rm.metas.load(gCtx, f.ref)
				if err != nil {
					return err
				}
				mu.Lock()
				byID[fm.FileID] = *fm
				walkOrder[f.index] = fm.FileID
				routing[fm.FileID] = prefixes[f.index]
				mu.Unlock()
				phase.Increment(1)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		phase.Error()
		return nil, nil, nil, err
	}
	phase.Done()
	return byID, walkOrder, routing, nil
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

// restoreOrder returns FileIDs in the order a restore should write them.
//
// The constraint is that an entry's parents exist before it does, so a directory
// is created before anything it contains. That constraint binds only the entries
// that *are* parents — everything else is a leaf, and a leaf may be written at
// any point after the interior of the tree exists.
//
// So the interior is emitted first, in parent-before-child order, and the leaves
// follow in walkOrder. Leaves are the overwhelming majority (50,000 of 50,500 in
// the tree RFC 0023 measured) and walk order is the order backup wrote them,
// which is the order they are laid out in packfiles. Ordering the whole tree
// topologically instead scattered reads across every pack: 55% pack-cache misses
// against 0.6% for the metadata phase, which already reads in walk order.
//
// Interior membership is derived from the data rather than from Type. A regular
// file is a leaf in every source model this supports, but a snapshot is data
// read off a store, and an entry that claims a file as its parent must still be
// ordered after it rather than trusted not to exist.
func restoreOrder(byID map[string]core.FileMeta, walkOrder []string) []string {
	interior := make(map[string]bool)
	for _, meta := range byID {
		for _, parentID := range meta.Parents {
			if _, ok := byID[parentID]; ok {
				interior[parentID] = true
			}
		}
	}

	out := make([]string, 0, len(byID))
	emitted := make(map[string]bool, len(byID))

	var visit func(core.FileMeta)
	visit = func(meta core.FileMeta) {
		if emitted[meta.FileID] {
			return
		}
		// Marked before recursing, not after: a parent cycle would otherwise
		// revisit this entry forever. Marking first makes a cycle terminate with
		// the entry emitted once, in an order the cycle itself does not define.
		emitted[meta.FileID] = true
		for _, parentID := range meta.Parents {
			if parent, ok := byID[parentID]; ok {
				visit(parent)
			}
		}
		out = append(out, meta.FileID)
	}

	// Interior nodes in walk order too, so that sibling directories are created
	// in the order they were backed up rather than in map-iteration order. visit
	// pulls in any ancestor that has not been reached yet.
	for _, id := range walkOrder {
		if interior[id] {
			visit(byID[id])
		}
	}
	// A cycle among interior nodes can leave one unreached from walkOrder if the
	// snapshot is malformed; nothing may be dropped.
	for id := range interior {
		if !emitted[id] {
			visit(byID[id])
		}
	}

	for _, id := range walkOrder {
		if emitted[id] {
			continue
		}
		emitted[id] = true
		out = append(out, id)
	}
	// walkOrder comes from the same walk that built byID, so this adds nothing in
	// practice. It is here because dropping an entry silently loses a file.
	for id := range byID {
		if !emitted[id] {
			emitted[id] = true
			out = append(out, id)
		}
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

// metaFetch pairs a ref with the slot its result belongs in. The slot is
// carried because fetch order and walk order are deliberately different: a ref
// fetched third may be the first entry the restore writes.
type metaFetch struct {
	ref   string
	index int
}

// indexGroups pairs each ref in a plan's groups with its original index.
//
// A ref can appear more than once when two entries share a filemeta object, so
// occurrences are handed out in order rather than looked up: the plan names the
// ref once per occurrence, and each occurrence needs its own slot.
func indexGroups(groups [][]string, refs []string) [][]metaFetch {
	byRef := make(map[string][]int, len(refs))
	for i, ref := range refs {
		byRef[ref] = append(byRef[ref], i)
	}
	out := make([][]metaFetch, 0, len(groups))
	for _, group := range groups {
		fetches := make([]metaFetch, 0, len(group))
		for _, ref := range group {
			idx := byRef[ref]
			if len(idx) == 0 {
				continue // a key the store invented; not possible for a partition
			}
			fetches = append(fetches, metaFetch{ref: ref, index: idx[0]})
			byRef[ref] = idx[1:]
		}
		if len(fetches) > 0 {
			out = append(out, fetches)
		}
	}
	return out
}
