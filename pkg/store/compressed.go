package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
)

// CompressedStore wraps an ObjectStore and transparently gzip-compresses on
// write and decompresses on read. Uncompressed data is returned as-is.
type CompressedStore struct {
	inner ObjectStore
}

func NewCompressedStore(inner ObjectStore) *CompressedStore {
	return &CompressedStore{inner: inner}
}

func (s *CompressedStore) Put(ctx context.Context, key string, data []byte) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	out := buf.Bytes()
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
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return data, nil
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}
