package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var (
	zstdEncoder *zstd.Encoder
	zstdDecoder *zstd.Decoder
	zstdOnce    sync.Once
)

func initZstd() {
	zstdOnce.Do(func() {
		// Use default compression which provides excellent speed/ratio.
		// Set concurrency to 1 since our chunker already uploads chunks concurrently!
		var err error
		zstdEncoder, err = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			panic(err) // Should not fail with default options
		}
		zstdDecoder, err = zstd.NewReader(nil)
		if err != nil {
			panic(err)
		}
	})
}

// CompressedStore wraps an ObjectStore and transparently zstd-compresses on
// write and decompresses (zstd or gzip) on read. Uncompressed data is returned as-is.
type CompressedStore struct {
	inner ObjectStore
}

func NewCompressedStore(inner ObjectStore) *CompressedStore {
	initZstd()
	return &CompressedStore{inner: inner}
}

func (s *CompressedStore) Put(ctx context.Context, key string, data []byte) error {
	out := zstdEncoder.EncodeAll(data, make([]byte, 0, len(data)))
	if len(out) >= len(data) {
		out = data
	}
	return s.inner.Put(ctx, key, out)
}

func (s *CompressedStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return maybeDecompress(data)
}

func (s *CompressedStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *CompressedStore) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

func (s *CompressedStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func (s *CompressedStore) Size(ctx context.Context, key string) (int64, error) {
	return s.inner.Size(ctx, key)
}

func (s *CompressedStore) TotalSize(ctx context.Context) (int64, error) {
	return s.inner.TotalSize(ctx)
}

func maybeDecompress(data []byte) ([]byte, error) {
	if len(data) < 2 {
		return data, nil
	}

	// Check gzip magic header (0x1f 0x8b)
	if data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return data, nil
		}
		defer func() { _ = zr.Close() }()
		return io.ReadAll(zr)
	}

	// Check zstd magic header (0x28 0xb5 0x2f 0xfd) Little-Endian
	if len(data) >= 4 && data[0] == 0x28 && data[1] == 0xb5 && data[2] == 0x2f && data[3] == 0xfd {
		initZstd()
		dec, err := zstdDecoder.DecodeAll(data, nil)
		if err != nil {
			// If not valid zstd, return as is (could be chunk content starting with same bytes)
			return data, nil
		}
		return dec, nil
	}

	return data, nil
}
