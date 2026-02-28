package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"

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
func (c *Chunker) ProcessStream(r io.Reader, onProgress func(int64)) (refs []string, size int64, hash string, err error) {
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

	ctx := context.Background()
	hasher := sha256.New()

	for {
		chunk, err := cdc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, "", err
		}

		if _, err := hasher.Write(chunk.Data); err != nil {
			return nil, 0, "", err
		}

		n := int64(chunk.Length)
		size += n
		if onProgress != nil {
			onProgress(n)
		}

		ref, err := c.storeChunk(ctx, chunk.Data)
		if err != nil {
			return nil, 0, "", err
		}
		refs = append(refs, ref)
	}

	hash = hex.EncodeToString(hasher.Sum(nil))
	return refs, size, hash, nil
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
