package engine

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/internal/ui"
)

// RestoreOption configures a restore operation.
type RestoreOption func(*restoreConfig)

type restoreConfig struct{}

// RestoreResult holds the outcome of a restore operation.
type RestoreResult struct {
	SnapshotRef  string
	Root         string
	FilesWritten int
	DirsWritten  int
	BytesWritten int64
	Errors       int
}

// RestoreManager recreates a snapshot's file tree as a ZIP archive.
type RestoreManager struct {
	store    store.ObjectStore
	tree     *hamt.Tree
	reporter ui.Reporter
}

func NewRestoreManager(s store.ObjectStore, reporter ui.Reporter) *RestoreManager {
	return &RestoreManager{
		store:    s,
		tree:     hamt.NewTree(s),
		reporter: reporter,
	}
}

// Run writes the snapshot's file tree as a ZIP archive to w.
// snapshotRef can be "", "latest", a bare hash, or "snapshot/<hash>".
func (rm *RestoreManager) Run(ctx context.Context, w io.Writer, snapshotRef string) (*RestoreResult, error) {
	snap, snapshotRef, err := rm.resolveSnapshot(ctx, snapshotRef)
	if err != nil {
		return nil, err
	}

	byID, err := rm.collectMetadata(snap.Root)
	if err != nil {
		return nil, err
	}

	cw := &countingWriter{w: w}
	zw := zip.NewWriter(cw)
	defer func() { _ = zw.Close() }()

	result := &RestoreResult{
		SnapshotRef: snapshotRef,
		Root:        snap.Root,
	}

	sorted := topoSort(byID)
	phase := rm.reporter.StartPhase("Restoring", int64(len(sorted)), false)

	for _, meta := range sorted {
		p := buildZipPath(meta, byID)

		if meta.Type == core.FileTypeFolder {
			header := &zip.FileHeader{Name: p + "/", Method: zip.Store}
			if meta.Mtime > 0 {
				header.Modified = time.Unix(meta.Mtime, 0)
			}
			if _, err := zw.CreateHeader(header); err != nil {
				phase.Log(fmt.Sprintf("Failed: %s: %v", p, err))
				result.Errors++
				phase.Increment(1)
				continue
			}
			result.DirsWritten++
			phase.Increment(1)
			continue
		}

		if meta.ContentHash == "" {
			phase.Increment(1)
			continue
		}

		header := &zip.FileHeader{Name: p, Method: zip.Deflate}
		if meta.Mtime > 0 {
			header.Modified = time.Unix(meta.Mtime, 0)
		}
		fw, err := zw.CreateHeader(header)
		if err != nil {
			phase.Log(fmt.Sprintf("Failed: %s: %v", p, err))
			result.Errors++
			phase.Increment(1)
			continue
		}

		if err := rm.writeFileContent(ctx, fw, meta.ContentHash); err != nil {
			phase.Log(fmt.Sprintf("Failed: %s: %v", p, err))
			result.Errors++
			phase.Increment(1)
			continue
		}
		result.FilesWritten++
		phase.Increment(1)
	}

	phase.Done()

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalize zip: %w", err)
	}
	result.BytesWritten = cw.count
	return result, nil
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

func (rm *RestoreManager) writeFileContent(ctx context.Context, w io.Writer, contentHash string) error {
	content, err := rm.loadContent(ctx, contentHash)
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

func buildZipPath(meta core.FileMeta, byID map[string]core.FileMeta) string {
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

	const workers = 20
	type result struct {
		meta core.FileMeta
		err  error
	}

	jobs := make(chan string, len(refs))
	results := make(chan result, len(refs))

	var wg sync.WaitGroup
	for range min(workers, len(refs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range jobs {
				fm, err := rm.loadMeta(ref)
				if err != nil {
					results <- result{err: err}
					continue
				}
				results <- result{meta: *fm}
			}
		}()
	}

	for _, ref := range refs {
		jobs <- ref
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	byID := make(map[string]core.FileMeta, len(refs))
	for res := range results {
		if res.err != nil {
			phase.Error()
			return nil, res.err
		}
		byID[res.meta.FileID] = res.meta
		phase.Increment(1)
	}
	phase.Done()
	return byID, nil
}

func (rm *RestoreManager) loadMeta(ref string) (*core.FileMeta, error) {
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
