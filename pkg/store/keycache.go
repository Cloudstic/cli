package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

var keysBucket = []byte("k")

// KeyCacheStore wraps an ObjectStore and caches key existence from List calls,
// so that Exists returns immediately for known keys. Thread-safe.
// Keys are stored in a temporary bbolt database to avoid holding potentially
// millions of strings on the Go heap.
type KeyCacheStore struct {
	inner          ObjectStore
	db             *bolt.DB
	dbPath         string
	listedPrefixes map[string]struct{}
	mu             sync.RWMutex // protects listedPrefixes only
	putFlight      singleflight.Group
}

func (s *KeyCacheStore) Unwrap() ObjectStore { return s.inner }

func NewKeyCacheStore(inner ObjectStore) (*KeyCacheStore, error) {
	f, err := os.CreateTemp("", "cloudstic-keycache-*.db")
	if err != nil {
		return nil, fmt.Errorf("keycache temp file: %w", err)
	}
	dbPath := f.Name()
	_ = f.Close()

	db, err := bolt.Open(dbPath, 0600, &bolt.Options{NoSync: true, NoFreelistSync: true})
	if err != nil {
		_ = os.Remove(dbPath)
		return nil, fmt.Errorf("keycache bolt open: %w", err)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(keysBucket)
		return err
	}); err != nil {
		_ = db.Close()
		_ = os.Remove(dbPath)
		return nil, fmt.Errorf("keycache create bucket: %w", err)
	}

	return &KeyCacheStore{
		inner:          inner,
		db:             db,
		dbPath:         dbPath,
		listedPrefixes: make(map[string]struct{}),
	}, nil
}

// Close releases the bbolt database and removes the temp file.
func (s *KeyCacheStore) Close() error {
	if s.db != nil {
		_ = s.db.Close()
	}
	if s.dbPath != "" {
		_ = os.Remove(s.dbPath)
	}
	return nil
}

func (s *KeyCacheStore) PreloadKeys(ctx context.Context, prefixes ...string) error {
	var g errgroup.Group
	for _, p := range prefixes {
		prefix := p
		g.Go(func() error {
			keys, err := s.inner.List(ctx, prefix)
			if err != nil {
				return err
			}
			if err := s.db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket(keysBucket)
				for _, key := range keys {
					if err := b.Put([]byte(key), []byte{}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
			s.mu.Lock()
			s.listedPrefixes[prefix] = struct{}{}
			s.mu.Unlock()
			return nil
		})
	}
	return g.Wait()
}

func (s *KeyCacheStore) hasKey(key string) bool {
	var found bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(keysBucket).Get([]byte(key)) != nil
		return nil
	})
	return found
}

func (s *KeyCacheStore) addKey(key string) {
	_ = s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(keysBucket).Put([]byte(key), []byte{})
	})
}

func (s *KeyCacheStore) removeKey(key string) {
	_ = s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(keysBucket).Delete([]byte(key))
	})
}

func (s *KeyCacheStore) Exists(ctx context.Context, key string) (bool, error) {
	if s.hasKey(key) {
		return true, nil
	}
	s.mu.RLock()
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
		if s.hasKey(key) {
			return nil
		}

		_, err, _ := s.putFlight.Do(key, func() (interface{}, error) {
			if s.hasKey(key) {
				return nil, nil
			}

			if err := s.inner.Put(ctx, key, data); err != nil {
				return nil, err
			}
			s.addKey(key)
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
	s.removeKey(key)
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

func (s *KeyCacheStore) Flush(ctx context.Context) error {
	if f, ok := s.inner.(interface{ Flush(context.Context) error }); ok {
		return f.Flush(ctx)
	}
	return nil
}
