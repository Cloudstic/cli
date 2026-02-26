package store

import (
	"context"
	"errors"
	"sync/atomic"
)

var ErrQuotaExceeded = errors.New("storage quota exceeded during backup")

// QuotaStore wraps an ObjectStore and cancels the backup context when
// cumulative bytes written exceed the remaining budget.
type QuotaStore struct {
	ObjectStore
	written atomic.Int64
	budget  int64
	cancel  context.CancelCauseFunc
}

func NewQuotaStore(inner ObjectStore, budget int64, cancel context.CancelCauseFunc) *QuotaStore {
	return &QuotaStore{ObjectStore: inner, budget: budget, cancel: cancel}
}

func (q *QuotaStore) Put(ctx context.Context, key string, data []byte) error {
	if err := q.ObjectStore.Put(ctx, key, data); err != nil {
		return err
	}
	if q.written.Add(int64(len(data))) > q.budget {
		q.cancel(ErrQuotaExceeded)
	}
	return nil
}

// Written returns the total bytes successfully written through this store.
func (q *QuotaStore) Written() int64 { return q.written.Load() }
