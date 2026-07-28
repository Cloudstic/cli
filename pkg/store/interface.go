package store

import (
	"context"
	"fmt"
)

// ObjectStore is the interface for content-addressable object storage.
// Keys are slash-separated paths like "chunk/<hash>" or "snapshot/<hash>".
type ObjectStore interface {
	// Put must not retain data beyond the call: read or copy it synchronously
	// before returning. Callers (chunking in particular) pool and reuse their
	// write buffers the instant Put returns, so an implementation that keeps
	// the slice — instead of the bytes — would see it mutated out from under
	// a previously "stored" value.
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Size(ctx context.Context, key string) (int64, error)
	TotalSize(ctx context.Context) (int64, error)
	Flush(ctx context.Context) error
}

// ConcurrencyHinter is an optional interface that ObjectStore implementations
// can implement to indicate the optimal number of concurrent operations.
// Remote stores (S3) benefit from high concurrency; local stores do not.
type ConcurrencyHinter interface {
	ConcurrencyHint() int
}

// Unwrapper is an optional interface for wrapper stores (CompressedStore,
// EncryptedStore, etc.) to expose their inner store for introspection.
type Unwrapper interface {
	Unwrap() ObjectStore
}

// RangeGetter is an optional interface for backends that can read a byte range
// without transferring the whole object. It lets PackStore read a packfile's
// trailing footer without downloading the entire 8 MB pack.
//
// Implementations return exactly length bytes starting at offset, or an error.
// Backends that cannot do this simply do not implement it; callers fall back to
// a full Get, which is correct everywhere and merely slower.
type RangeGetter interface {
	GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error)
}

// GetConcurrencyHint walks the store wrapper chain and returns the first
// ConcurrencyHint it finds, defaulting to defaultConcurrency if none exists.
func GetConcurrencyHint(s ObjectStore, defaultConcurrency int) int {
	for s != nil {
		if h, ok := s.(ConcurrencyHinter); ok {
			return h.ConcurrencyHint()
		}
		if u, ok := s.(Unwrapper); ok {
			s = u.Unwrap()
		} else {
			break
		}
	}
	return defaultConcurrency
}

// HTTPRangeHeader renders an inclusive byte range for backends that speak HTTP
// range requests, for use when implementing RangeGetter. The end offset is
// inclusive, which is the classic off-by-one in this header.
func HTTPRangeHeader(offset, length int64) string {
	return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
}

// KeySlotPrefix is the object key prefix for encryption key slot objects.
// These objects are stored unencrypted (they contain already-wrapped keys)
// so they can be read without the encryption key — avoiding a chicken-and-egg
// problem during key loading.
//
// It lives in the contract package rather than with the encryption layer
// because it is a repository key-namespace fact that pkg/keychain also needs,
// alongside the chunk/, snapshot/ and index/ conventions.
const KeySlotPrefix = "keys/"
