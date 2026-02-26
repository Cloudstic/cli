package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"sync/atomic"
)

// CompressedStore wraps an ObjectStore and transparently compresses data on
// write and decompresses on read. Old uncompressed data is detected by the
// absence of a gzip header and returned as-is, making this backward compatible
// for reads.
type CompressedStore struct {
	inner          ObjectStore
	bytesIn        atomic.Int64
	bytesOut       atomic.Int64
}

func NewCompressedStore(inner ObjectStore) *CompressedStore {
	return &CompressedStore{inner: inner}
}

func (s *CompressedStore) Put(ctx context.Context, key string, data []byte) error {
	s.bytesIn.Add(int64(len(data)))

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
	s.bytesOut.Add(int64(len(out)))
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

// Stats returns the total uncompressed bytes in and compressed bytes out
// seen by Put since the last ResetStats call.
func (s *CompressedStore) Stats() (bytesIn, bytesOut int64) {
	return s.bytesIn.Load(), s.bytesOut.Load()
}

func (s *CompressedStore) ResetStats() {
	s.bytesIn.Store(0)
	s.bytesOut.Store(0)
}

// UnwrapCompressedStore walks the store wrapper chain to find a
// CompressedStore. Returns nil, false if none is found.
func UnwrapCompressedStore(s ObjectStore) (*CompressedStore, bool) {
	switch v := s.(type) {
	case *CompressedStore:
		return v, true
	case *KeyCacheStore:
		return UnwrapCompressedStore(v.inner)
	case *MeteredStore:
		return UnwrapCompressedStore(v.ObjectStore)
	case *EncryptedStore:
		return UnwrapCompressedStore(v.ObjectStore)
	default:
		return nil, false
	}
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
