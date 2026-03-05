package store

import (
	"context"
)

// ObjectStore is the interface for content-addressable object storage.
// Keys are slash-separated paths like "chunk/<hash>" or "snapshot/<hash>".
type ObjectStore interface {
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
