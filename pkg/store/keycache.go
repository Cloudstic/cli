package store

import (
	"context"
	"sync"
)

// KeyCacheStore wraps an ObjectStore and caches key existence from List calls.
// After PreloadKeys, Exists returns immediately for keys known to be present,
// avoiding individual network round trips. Keys created via Put are
// automatically added to the cache. Thread-safe.
type KeyCacheStore struct {
	inner     ObjectStore
	knownKeys map[string]struct{}
	mu        sync.RWMutex
}

func NewKeyCacheStore(inner ObjectStore) *KeyCacheStore {
	return &KeyCacheStore{
		inner:     inner,
		knownKeys: make(map[string]struct{}),
	}
}

// PreloadKeys lists all keys under the given prefixes and caches them.
func (s *KeyCacheStore) PreloadKeys(ctx context.Context, prefixes ...string) error {
	for _, prefix := range prefixes {
		keys, err := s.inner.List(ctx, prefix)
		if err != nil {
			return err
		}
		s.mu.Lock()
		for _, key := range keys {
			s.knownKeys[key] = struct{}{}
		}
		s.mu.Unlock()
	}
	return nil
}

func (s *KeyCacheStore) Exists(ctx context.Context, key string) (bool, error) {
	s.mu.RLock()
	_, ok := s.knownKeys[key]
	s.mu.RUnlock()
	if ok {
		return true, nil
	}
	return s.inner.Exists(ctx, key)
}

func (s *KeyCacheStore) Put(ctx context.Context, key string, data []byte) error {
	if err := s.inner.Put(ctx, key, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.knownKeys[key] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *KeyCacheStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *KeyCacheStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	delete(s.knownKeys, key)
	s.mu.Unlock()
	return s.inner.Delete(ctx, key)
}

func (s *KeyCacheStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func (s *KeyCacheStore) Size(ctx context.Context, key string) (int64, error) {
	return s.inner.Size(ctx, key)
}

func (s *KeyCacheStore) TotalSize(ctx context.Context) (int64, error) {
	return s.inner.TotalSize(ctx)
}
