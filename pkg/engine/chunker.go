package engine

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"

	"github.com/cloudstic/cli/pkg/core"
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

// Chunker splits a byte stream into content-defined chunks, compresses and
// deduplicates them, and persists the resulting Content object.
type Chunker struct {
	store store.ObjectStore
}

func NewChunker(s store.ObjectStore) *Chunker {
	return &Chunker{store: s}
}

// ProcessStream splits r into content-defined chunks, compresses each one, and
// stores it (skipping duplicates). It returns the ordered chunk refs, total
// byte count, the raw and compressed sizes of truly new chunks, and the SHA-256
// content hash over the raw stream.
//
// onProgress is called after each chunk with the number of raw bytes consumed.
func (c *Chunker) ProcessStream(r io.Reader, onProgress func(int64)) (refs []string, size int64, newBytes int64, newBytesCompressed int64, hash string, err error) {
	cdcMu.Lock()
	cdc, err := fastcdc.NewChunker(r, fastcdc.Options{
		MinSize:     cdcMinSize,
		AverageSize: cdcAvgSize,
		MaxSize:     cdcMaxSize,
	})
	cdcMu.Unlock()
	if err != nil {
		return nil, 0, 0, 0, "", err
	}

	ctx := context.Background()
	hasher := sha256.New()

	for {
		chunk, err := cdc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, 0, 0, "", err
		}

		if _, err := hasher.Write(chunk.Data); err != nil {
			return nil, 0, 0, 0, "", err
		}

		n := int64(chunk.Length)
		size += n
		if onProgress != nil {
			onProgress(n)
		}

		ref, raw, compressed, err := c.storeChunk(ctx, chunk.Data)
		if err != nil {
			return nil, 0, 0, 0, "", err
		}
		newBytes += raw
		newBytesCompressed += compressed
		refs = append(refs, ref)
	}

	hash = hex.EncodeToString(hasher.Sum(nil))
	return refs, size, newBytes, newBytesCompressed, hash, nil
}

// CreateContentObject persists a Content object keyed by contentHash and
// returns its store ref.
func (c *Chunker) CreateContentObject(chunkRefs []string, size int64, contentHash string) (string, error) {
	content := core.Content{
		Type:   core.ObjectTypeContent,
		Size:   size,
		Chunks: chunkRefs,
	}

	data, err := json.Marshal(content)
	if err != nil {
		return "", err
	}

	ref := "content/" + contentHash
	if err := c.store.Put(context.Background(), ref, data); err != nil {
		return "", err
	}
	return ref, nil
}

// storeChunk compresses data with gzip and writes it to chunk/<hash>,
// skipping the write if the key already exists (dedup).
// Returns the raw and compressed sizes of newly stored data (both zero when deduped).
func (c *Chunker) storeChunk(ctx context.Context, data []byte) (ref string, rawNew int64, compressedNew int64, err error) {
	ref = "chunk/" + core.ComputeHash(data)

	exists, err := c.store.Exists(ctx, ref)
	if err != nil {
		return "", 0, 0, err
	}
	if exists {
		return ref, 0, 0, nil
	}

	compressed, err := gzipCompress(data)
	if err != nil {
		return "", 0, 0, err
	}
	if err := c.store.Put(ctx, ref, compressed); err != nil {
		return "", 0, 0, err
	}
	return ref, int64(len(data)), int64(len(compressed)), nil
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
