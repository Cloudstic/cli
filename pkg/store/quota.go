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

func (q *QuotaStore) Unwrap() ObjectStore { return q.ObjectStore }

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

// DeleteAll implements BatchDeleter by forwarding to the wrapped store. The
// quota counts bytes written, so deletion is a passthrough here — but the
// method must exist, or wrapping a backend in a budget would quietly cost a
// prune its batching, since an embedded ObjectStore promotes only the methods
// ObjectStore declares.
func (q *QuotaStore) DeleteAll(ctx context.Context, keys []string) error {
	return DeleteAll(ctx, q.ObjectStore, keys)
}

// DeleteAllSized implements SizedBatchDeleter by forwarding, so the sizes a
// listing supplied reach the meters beneath the budget.
func (q *QuotaStore) DeleteAllSized(ctx context.Context, objects []SizedKey) error {
	return DeleteAllSized(ctx, q.ObjectStore, objects)
}

// ListSized implements SizedLister by forwarding, for the reason DeleteAll
// gives: an embedded ObjectStore promotes only the methods ObjectStore
// declares, so a capability not restated here stops here.
func (q *QuotaStore) ListSized(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	return ListSized(ctx, q.ObjectStore, prefix, fn)
}
