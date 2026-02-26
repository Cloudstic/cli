package engine

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/hamt"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/ui"
)

// RestoreOption configures a restore operation.
type RestoreOption func(*restoreConfig)

type restoreConfig struct{}

// RestoreResult holds the outcome of a restore operation.
type RestoreResult struct {
	SnapshotRef string
	Root        string
	TargetDir   string
	Restored    int
	Errors      int
}

// RestoreManager recreates a snapshot's file tree on the local filesystem.
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

// Run restores the given snapshot (or the latest) into targetDir.
func (rm *RestoreManager) Run(ctx context.Context, targetDir, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	snap, snapshotRef, err := rm.resolveSnapshot(snapshotRef)
	if err != nil {
		return nil, err
	}

	byID, err := rm.collectMetadata(snap.Root)
	if err != nil {
		return nil, err
	}

	phase := rm.reporter.StartPhase("Restoring", int64(len(byID)), false)

	result := &RestoreResult{
		SnapshotRef: snapshotRef,
		Root:        snap.Root,
		TargetDir:   targetDir,
	}

	sorted := topoSort(byID)
	for _, meta := range sorted {
		path := buildPath(meta, byID)
		target := filepath.Join(targetDir, path)

		if err := rm.restoreEntry(meta, target); err != nil {
			phase.Log(fmt.Sprintf("Failed: %s: %v", path, err))
			result.Errors++
			phase.Increment(1)
			continue
		}
		result.Restored++
		phase.Increment(1)
	}

	phase.Done()
	return result, nil
}

// RestoreToZip writes the snapshot's file tree as a ZIP archive to w.
func (rm *RestoreManager) RestoreToZip(ctx context.Context, w io.Writer, snapshotHash string) error {
	snapshotRef := "snapshot/" + snapshotHash
	snap, _, err := rm.resolveSnapshot(snapshotRef)
	if err != nil {
		return err
	}

	byID, err := rm.collectMetadata(snap.Root)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	sorted := topoSort(byID)
	for _, meta := range sorted {
		p := buildZipPath(meta, byID)

		if meta.Type == core.FileTypeFolder {
			header := &zip.FileHeader{Name: p + "/", Method: zip.Store}
			if meta.Mtime > 0 {
				header.Modified = time.Unix(meta.Mtime, 0)
			}
			if _, err := zw.CreateHeader(header); err != nil {
				return fmt.Errorf("create zip dir %s: %w", p, err)
			}
			continue
		}

		if meta.ContentHash == "" {
			continue
		}

		header := &zip.FileHeader{Name: p, Method: zip.Deflate}
		if meta.Mtime > 0 {
			header.Modified = time.Unix(meta.Mtime, 0)
		}
		fw, err := zw.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("create zip entry %s: %w", p, err)
		}

		if err := rm.writeFileContent(fw, meta.ContentHash); err != nil {
			return fmt.Errorf("write zip entry %s: %w", p, err)
		}
	}

	return zw.Close()
}

func (rm *RestoreManager) writeFileContent(w io.Writer, contentHash string) error {
	content, err := rm.loadContent(contentHash)
	if err != nil {
		return err
	}
	for _, chunkRef := range content.Chunks {
		if err := rm.writeChunk(w, chunkRef); err != nil {
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

// buildZipPath is like buildPath but always uses forward slashes for ZIP compatibility.
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

func (rm *RestoreManager) resolveSnapshot(ref string) (*core.Snapshot, string, error) {
	if ref == "" {
		data, err := rm.store.Get("index/latest")
		if err != nil {
			return nil, "", fmt.Errorf("cannot find latest index: %w", err)
		}
		var idx core.Index
		if err := json.Unmarshal(data, &idx); err != nil {
			return nil, "", err
		}
		ref = idx.LatestSnapshot
	}

	data, err := rm.store.Get(ref)
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

// collectMetadata walks the HAMT and returns all entries keyed by FileID.
func (rm *RestoreManager) collectMetadata(root string) (byID map[string]core.FileMeta, err error) {
	byID = make(map[string]core.FileMeta)

	err = rm.tree.Walk(root, func(_, valueRef string) error {
		fm, err := rm.loadMeta(valueRef)
		if err != nil {
			return err
		}
		byID[fm.FileID] = *fm
		return nil
	})
	return byID, err
}

func (rm *RestoreManager) loadMeta(ref string) (*core.FileMeta, error) {
	data, err := rm.store.Get(ref)
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
// Ordering & path building
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

// buildPath walks the parent chain to reconstruct the full relative path.
// Parents contain FileIDs, resolved via byID.
func buildPath(meta core.FileMeta, byID map[string]core.FileMeta) string {
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
	return strings.Join(parts, string(filepath.Separator))
}

// ---------------------------------------------------------------------------
// File restoration
// ---------------------------------------------------------------------------

func (rm *RestoreManager) restoreEntry(meta core.FileMeta, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}

	if meta.Type == core.FileTypeFolder {
		return rm.restoreFolder(meta, target)
	}
	if meta.ContentHash == "" {
		return nil
	}
	return rm.restoreFile(meta, target)
}

func (rm *RestoreManager) restoreFolder(meta core.FileMeta, target string) error {
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	setMtime(target, meta.Mtime)
	return nil
}

func (rm *RestoreManager) restoreFile(meta core.FileMeta, target string) error {
	content, err := rm.loadContent(meta.ContentHash)
	if err != nil {
		return err
	}

	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	for _, chunkRef := range content.Chunks {
		if err := rm.writeChunk(f, chunkRef); err != nil {
			return err
		}
	}

	if len(content.DataInlineB64) > 0 {
		if _, err := f.Write(content.DataInlineB64); err != nil {
			return err
		}
	}

	setMtime(target, meta.Mtime)
	return nil
}

func (rm *RestoreManager) loadContent(hash string) (*core.Content, error) {
	data, err := rm.store.Get("content/" + hash)
	if err != nil {
		return nil, err
	}
	var c core.Content
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (rm *RestoreManager) writeChunk(w io.Writer, ref string) error {
	data, err := rm.store.Get(ref)
	if err != nil {
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decompress chunk %s: %w", ref, err)
	}
	defer func() { _ = zr.Close() }()

	_, err = io.Copy(w, zr)
	return err
}

func setMtime(path string, mtime int64) {
	if mtime <= 0 {
		return
	}
	t := time.Unix(mtime, 0)
	_ = os.Chtimes(path, t, t)
}
