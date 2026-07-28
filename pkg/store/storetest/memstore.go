package storetest

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// MemStore is an in-memory ObjectStore for tests. It is safe for concurrent
// use, so it can stand in for a real backend under -race.
type MemStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{data: make(map[string][]byte)}
}

func (m *MemStore) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Copy: callers pool and reuse their write buffers the instant Put
	// returns, per the ObjectStore contract.
	m.data[key] = append([]byte(nil), data...)
	return nil
}

func (m *MemStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return append([]byte(nil), d...), nil
}

func (m *MemStore) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *MemStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *MemStore) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *MemStore) Size(_ context.Context, key string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.data[key]
	if !ok {
		return 0, fmt.Errorf("not found: %s", key)
	}
	return int64(len(d)), nil
}

func (m *MemStore) TotalSize(_ context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total int64
	for _, d := range m.data {
		total += int64(len(d))
	}
	return total, nil
}

func (m *MemStore) Flush(_ context.Context) error { return nil }

// Len reports how many objects are stored, for assertions.
func (m *MemStore) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.data)
}
