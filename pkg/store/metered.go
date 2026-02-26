package store

import (
	"context"
	"strings"
	"sync/atomic"
)

// MeteredStore wraps an ObjectStore and tracks the total number of bytes
// written via Put and deleted via Delete. It is safe for concurrent use.
type MeteredStore struct {
	ObjectStore
	bytesWritten atomic.Int64
}

func NewMeteredStore(s ObjectStore) *MeteredStore {
	return &MeteredStore{ObjectStore: s}
}

func (m *MeteredStore) Delete(ctx context.Context, key string) error {
	_, err := m.DeleteReturnSize(ctx, key)
	return err
}

func (m *MeteredStore) DeleteReturnSize(ctx context.Context, key string) (int64, error) {

	size := int64(0)
	if !strings.HasPrefix(key, "index/") {
		s, err := m.Size(ctx, key)
		if err != nil {
			return 0, err
		}
		size = s
	}

	if err := m.ObjectStore.Delete(ctx, key); err != nil {
		return 0, err
	}
	m.bytesWritten.Add(-size)
	return size, nil
}

func (m *MeteredStore) Put(ctx context.Context, key string, data []byte) error {

	if err := m.ObjectStore.Put(ctx, key, data); err != nil {
		return err
	}
	if strings.HasPrefix(key, "index/") {
		return nil
	}
	size, err := m.Size(ctx, key)
	if err != nil {
		return err
	}
	m.bytesWritten.Add(size)
	return nil
}

func (m *MeteredStore) BytesWritten() int64 {
	return m.bytesWritten.Load()
}

func (m *MeteredStore) Reset() {
	m.bytesWritten.Store(0)
}
