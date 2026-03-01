package engine

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

const defaultUploadConcurrency = 10

// inlineThreshold is the maximum file size for which content is stored inline
// in the Content object rather than as separate chunk objects.
const inlineThreshold = 512 * 1024 // 512 KiB (matches CDC min chunk size)

var inlineBufferPool = sync.Pool{
	New: func() interface{} {
		// Pre-allocate a buffer large enough for inlineThreshold
		b := make([]byte, inlineThreshold)
		return &b
	},
}

type uploadResult struct {
	fileID        string
	ref           string
	meta          core.FileMeta
	contentRef    string   // content key to cache (empty when dedup'd)
	contentChunks []string // chunk refs for the content entry (nil for inline)
	err           error
}

// upload processes the pending file queue with concurrent workers, inserts each
// result into the HAMT, and returns the updated root.
func (bm *BackupManager) upload(ctx context.Context, pending []core.FileMeta, totalBytes int64, root string) (string, error) {
	if len(pending) == 0 {
		return root, nil
	}

	phase := bm.reporter.StartPhase("Uploading", totalBytes, true)

	concurrency := store.GetConcurrencyHint(bm.store, defaultUploadConcurrency)

	// Cap max memory in-flight to 150 MB to prevent OOMs on highly concurrent stores (S3)
	maxInFlight := int64(150 * 1024 * 1024)
	sem := semaphore.NewWeighted(maxInFlight)

	jobs := make(chan core.FileMeta, min(128, len(pending)))
	results := make(chan uploadResult, min(128, len(pending)))

	for range min(concurrency, len(pending)) {
		go func() {
			for meta := range jobs {
				weight := meta.Size
				// Large files are streamed in chunks, so they don't consume `meta.Size` RAM all at once.
				// Cap weight at 10MB to allow other files to process alongside large ones.
				if weight > 10*1024*1024 {
					weight = 10 * 1024 * 1024
				} else if weight <= 0 {
					weight = 1024 // min weight
				}

				_ = sem.Acquire(ctx, weight)
				res := bm.processFile(ctx, meta, phase)
				sem.Release(weight)

				results <- res
			}
		}()
	}

	go func() {
		for _, m := range pending {
			jobs <- m
		}
		close(jobs)
	}()

	var err error
	for range pending {
		res := <-results
		if res.err != nil {
			phase.Error()
			return "", res.err
		}
		root, err = bm.tree.Insert(root, res.fileID, res.ref)
		if err != nil {
			phase.Error()
			return "", fmt.Errorf("hamt insert: %w", err)
		}
		bm.newMetas[res.ref] = res.meta
	}

	phase.Done()
	return root, nil
}

// processFile uploads (or deduplicates) a single file's content and persists
// its FileMeta. It is safe to call from multiple goroutines.
func (bm *BackupManager) processFile(ctx context.Context, meta core.FileMeta, phase ui.Phase) uploadResult {
	if bm.cfg.verbose {
		phase.Log(fmt.Sprintf("Processing: %s", meta.Name))
	}

	contentHash, size, contentRef, contentChunks, err := bm.uploadContent(ctx, meta, phase)
	if err != nil {
		return uploadResult{err: err}
	}

	meta.ContentHash = contentHash
	meta.Size = size

	metaRef, metaData, err := meta.Ref()
	if err != nil {
		return uploadResult{err: err}
	}
	if err := bm.store.Put(ctx, metaRef, metaData); err != nil {
		return uploadResult{err: err}
	}
	return uploadResult{
		fileID:        meta.FileID,
		ref:           metaRef,
		meta:          meta,
		contentRef:    contentRef,
		contentChunks: contentChunks,
	}
}

// uploadContent streams, chunks, and stores the file content. If the content
// already exists (dedup by hash), the upload is skipped and contentRef is empty.
// Small files (below inlineThreshold) are stored inline in the Content object
// to reduce the number of backend API calls.
func (bm *BackupManager) uploadContent(ctx context.Context, meta core.FileMeta, phase ui.Phase) (hash string, size int64, contentRef string, contentChunks []string, err error) {
	if meta.ContentHash != "" {
		exists, err := bm.store.Exists(ctx, "content/"+meta.ContentHash)
		if err == nil && exists {
			if bm.cfg.verbose {
				phase.Log(fmt.Sprintf("Deduplicated: %s", meta.Name))
			}
			return meta.ContentHash, meta.Size, "", nil, nil
		}
	}

	rc, err := bm.source.GetFileStream(meta.FileID)
	if err != nil {
		return "", 0, "", nil, fmt.Errorf("get stream for %s: %w", meta.FileID, err)
	}
	defer func() { _ = rc.Close() }()

	if meta.Size > 0 && meta.Size <= inlineThreshold {
		return bm.uploadInline(ctx, rc, meta, phase)
	}

	chunkRefs, size, hash, err := bm.chunker.ProcessStream(rc, func(n int64) {
		phase.Increment(n)
	})
	if err != nil {
		return "", 0, "", nil, fmt.Errorf("chunking %s: %w", meta.Name, err)
	}

	ref, err := bm.chunker.CreateContentObject(chunkRefs, size, hash)
	if err != nil {
		return "", 0, "", nil, fmt.Errorf("create content for %s: %w", meta.Name, err)
	}
	return hash, size, ref, chunkRefs, nil
}

// uploadInline reads the entire file into memory and stores it directly inside
// the Content object, bypassing the chunker. Uses a sync.Pool to minimize allocations.
func (bm *BackupManager) uploadInline(ctx context.Context, r io.Reader, meta core.FileMeta, phase ui.Phase) (hash string, size int64, contentRef string, contentChunks []string, err error) {
	bufPtr := inlineBufferPool.Get().(*[]byte)
	buf := *bufPtr
	defer inlineBufferPool.Put(bufPtr)

	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", 0, "", nil, fmt.Errorf("read %s: %w", meta.Name, err)
	}

	data := buf[:n]
	size = int64(n)
	phase.Increment(size)
	hash = core.ComputeHash(data)

	contentKey := "content/" + hash
	if exists, _ := bm.store.Exists(ctx, contentKey); exists {
		if bm.cfg.verbose {
			phase.Log(fmt.Sprintf("Deduplicated: %s", meta.Name))
		}
		return hash, size, "", nil, nil
	}

	// Manually construct JSON to avoid json.Marshal allocating a huge string for the base64 data
	encodedLen := base64.StdEncoding.EncodedLen(len(data))
	prefix := fmt.Sprintf(`{"type":"content","size":%d,"data_inline_b64":"`, size)
	suffix := `"}`

	contentData := make([]byte, len(prefix)+encodedLen+len(suffix))
	copy(contentData, prefix)
	base64.StdEncoding.Encode(contentData[len(prefix):], data)
	copy(contentData[len(prefix)+encodedLen:], suffix)

	if err := bm.store.Put(ctx, contentKey, contentData); err != nil {
		return "", 0, "", nil, err
	}
	return hash, size, contentKey, nil, nil
}
