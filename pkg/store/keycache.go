package store

import (
	"context"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
)

// KeyCacheStore wraps an ObjectStore and caches key existence from List calls,
// so that Exists returns immediately for known keys. Thread-safe.
type KeyCacheStore struct {
	inner          ObjectStore
	knownKeys      map[string]struct{}
	listedPrefixes map[string]struct{}
	mu             sync.RWMutex
	putFlight      singleflight.Group
}

func (s *KeyCacheStore) Unwrap() ObjectStore { return s.inner }

func NewKeyCacheStore(inner ObjectStore) *KeyCacheStore {
	return &KeyCacheStore{
		inner:          inner,
		knownKeys:      make(map[string]struct{}),
		listedPrefixes: make(map[string]struct{}),
	}
}

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
		s.listedPrefixes[prefix] = struct{}{}
		s.mu.Unlock()
	}
	return nil
}

func (s *KeyCacheStore) Exists(ctx context.Context, key string) (bool, error) {
	s.mu.RLock()
	_, ok := s.knownKeys[key]
	if ok {
		s.mu.RUnlock()
		return true, nil
	}
	for prefix := range s.listedPrefixes {
		if strings.HasPrefix(key, prefix) {
			s.mu.RUnlock()
			return false, nil
		}
	}
	s.mu.RUnlock()
	return s.inner.Exists(ctx, key)
}

func (s *KeyCacheStore) Put(ctx context.Context, key string, data []byte) error {
	if s.isContentAddressed(key) {
		s.mu.RLock()
		_, known := s.knownKeys[key]
		s.mu.RUnlock()
		if known {
			return nil
		}

		_, err, _ := s.putFlight.Do(key, func() (interface{}, error) {
			s.mu.RLock()
			_, already := s.knownKeys[key]
			s.mu.RUnlock()
			if already {
				return nil, nil
			}
			if err := s.inner.Put(ctx, key, data); err != nil {
				return nil, err
			}
			s.mu.Lock()
			s.knownKeys[key] = struct{}{}
			s.mu.Unlock()
			return nil, nil
		})
		return err
	}

	return s.inner.Put(ctx, key, data)
}

// isContentAddressed returns true for keys under prefixes that were listed,
// which are immutable (same key = same data). Mutable keys like index/*
// must always be written through.
func (s *KeyCacheStore) isContentAddressed(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for prefix := range s.listedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
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
