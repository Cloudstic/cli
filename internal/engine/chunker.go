package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"

	"github.com/jotfs/fastcdc-go"
)

// fastcdc-go mutates a package-level lookup table inside NewChunker,
// so concurrent calls race. Serialize creation to avoid this.
var cdcMu sync.Mutex

// FastCDC boundary sizes.
const (
	cdcMinSize = 512 * 1024      // 512 KiB
	cdcAvgSize = 1 * 1024 * 1024 // 1 MiB
	cdcMaxSize = 8 * 1024 * 1024 // 8 MiB
)

// chunkBufPool pools the up-to-8MiB copies ProcessStream makes of each
// chunk before handing it to a storage worker. Without pooling, the 32-slot
// job channel plus per-worker in-flight chunks can pin on the order of
// hundreds of MB per file being chunked concurrently, all freshly allocated
// and GC'd per chunk. Every store.Put in the chain below (compression,
// encryption, packing, the backend) consumes its data argument synchronously
// before returning — none retain the slice past the call — so it is safe to
// return a buffer to the pool the moment storeChunk completes.
var chunkBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, cdcMaxSize)
		return &b
	},
}

// Chunker splits a byte stream into content-defined chunks, deduplicates
// them, and persists the resulting Content object.
type Chunker struct {
	store   store.ObjectStore
	hmacKey []byte
}

func NewChunker(s store.ObjectStore, hmacKey []byte) *Chunker {
	return &Chunker{store: s, hmacKey: hmacKey}
}

// ProcessStream splits r into content-defined chunks and stores each one
// (skipping duplicates). It returns the ordered chunk refs, total byte count,
// and the SHA-256 content hash over the raw stream.
//
// onProgress is called after each chunk with the number of raw bytes consumed.
func (c *Chunker) ProcessStream(ctx context.Context, r io.Reader, onProgress func(int64)) (refs []string, size int64, hash string, err error) {
	cdcMu.Lock()
	cdc, err := fastcdc.NewChunker(r, fastcdc.Options{
		MinSize:     cdcMinSize,
		AverageSize: cdcAvgSize,
		MaxSize:     cdcMaxSize,
	})
	cdcMu.Unlock()
	if err != nil {
		return nil, 0, "", err
	}

	hasher := sha256.New()

	type chunkJob struct {
		index  int
		data   []byte
		bufPtr *[]byte // underlying pooled buffer backing data; returned to chunkBufPool once storeChunk completes
	}
	type chunkResult struct {
		index int
		ref   string
	}

	g, gCtx := errgroup.WithContext(ctx)
	jobs := make(chan chunkJob, 32)

	// Collect results directly into a pre-allocated or dynamically grown slice
	// protected by a mutex, avoiding complex channel synchronization deadlocks.
	var resultsMu sync.Mutex
	var collectedResults []chunkResult

	// Worker pool for storing chunks concurrently
	numWorkers := store.GetConcurrencyHint(c.store, 10)
	for i := 0; i < numWorkers; i++ {
		g.Go(func() error {
			for job := range jobs {
				ref, err := c.storeChunk(gCtx, job.data)
				chunkBufPool.Put(job.bufPtr)
				if err != nil {
					return err
				}
				resultsMu.Lock()
				collectedResults = append(collectedResults, chunkResult{index: job.index, ref: ref})
				resultsMu.Unlock()
			}
			return nil
		})
	}

	// Read chunks sequentially to maintain order and update overall hash
	var totalChunks int
	g.Go(func() error {
		defer close(jobs)
		for {
			chunk, err := cdc.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}

			if _, err := hasher.Write(chunk.Data); err != nil {
				return err
			}

			n := int64(chunk.Length)
			size += n
			if onProgress != nil {
				onProgress(n)
			}

			// Copy data so it is safe to process asynchronously, from a pooled
			// buffer so repeated up-to-8MiB allocations don't pile up as GC
			// churn while many chunks are in flight.
			bufPtr := chunkBufPool.Get().(*[]byte)
			dataCopy := (*bufPtr)[:len(chunk.Data)]
			copy(dataCopy, chunk.Data)

			select {
			case jobs <- chunkJob{index: totalChunks, data: dataCopy, bufPtr: bufPtr}:
				totalChunks++
			case <-gCtx.Done():
				chunkBufPool.Put(bufPtr)
				return gCtx.Err()
			}
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, 0, "", err
	}

	refs = make([]string, totalChunks)
	for _, res := range collectedResults {
		refs[res.index] = res.ref
	}

	hash = hex.EncodeToString(hasher.Sum(nil))
	return refs, size, hash, nil
}

// CreateContentObject persists a Content object. The object is keyed by an HMAC
// of the contentHash (if encryption is enabled) to prevent hash leakage, or the
// plain contentHash otherwise. Returns the secure contentRef (the hex hash used).
func (c *Chunker) CreateContentObject(ctx context.Context, chunkRefs []string, size int64, contentHash string) (string, error) {
	content := core.Content{
		Type:   core.ObjectTypeContent,
		Size:   size,
		Chunks: chunkRefs,
	}

	data, err := json.Marshal(content)
	if err != nil {
		return "", err
	}

	var contentRef string
	if len(c.hmacKey) > 0 {
		contentRef = crypto.ComputeHMAC(c.hmacKey, []byte(contentHash))
	} else {
		contentRef = contentHash
	}

	ref := "content/" + contentRef
	if err := c.store.Put(ctx, ref, data); err != nil {
		return "", err
	}
	return contentRef, nil
}

func (c *Chunker) storeChunk(ctx context.Context, data []byte) (string, error) {
	var ref string
	if len(c.hmacKey) > 0 {
		ref = "chunk/" + crypto.ComputeHMAC(c.hmacKey, data)
	} else {
		ref = "chunk/" + core.ComputeHash(data)
	}

	exists, err := c.store.Exists(ctx, ref)
	if err != nil {
		return "", err
	}
	if exists {
		return ref, nil
	}

	if err := c.store.Put(ctx, ref, data); err != nil {
		return "", err
	}
	return ref, nil
}
