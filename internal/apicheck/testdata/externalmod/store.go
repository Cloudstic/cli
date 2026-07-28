package externalmod

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudstic/cli/pkg/store"
)

// Store implements store.ObjectStore using only
// github.com/cloudstic/cli/pkg/store — no internal/ package, and no AWS SDK.
//
// It also implements RangeGetter, the optional capability interface a backend
// opts into so PackStore can read a packfile footer without transferring the
// whole pack.
type Store struct {
	mu   sync.RWMutex
	data map[string][]byte
}

var (
	_ store.ObjectStore = (*Store)(nil)
	_ store.RangeGetter = (*Store)(nil)
)

func (s *Store) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	// Copy: the contract says Put must not retain the caller's slice.
	s.data[key] = append([]byte(nil), data...)
	return nil
}

func (s *Store) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.data[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", store.ErrNotFound, key)
	}
	return append([]byte(nil), d...), nil
}

func (s *Store) GetRange(_ context.Context, key string, offset, length int64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.data[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", store.ErrNotFound, key)
	}
	if offset < 0 || length < 0 || offset+length > int64(len(d)) {
		return nil, fmt.Errorf("range %d+%d out of bounds for %s", offset, length, key)
	}
	return append([]byte(nil), d[offset:offset+length]...), nil
}

func (s *Store) Exists(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[key]
	return ok, nil
}

func (s *Store) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *Store) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (s *Store) Size(_ context.Context, key string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.data[key]
	if !ok {
		return 0, fmt.Errorf("%w: %s", store.ErrNotFound, key)
	}
	return int64(len(d)), nil
}

func (s *Store) TotalSize(context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for _, d := range s.data {
		total += int64(len(d))
	}
	return total, nil
}

func (s *Store) Flush(context.Context) error { return nil }
