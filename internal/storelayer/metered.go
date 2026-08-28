package storelayer

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/cloudstic/cli/pkg/store"
)

// MeteredStore wraps an store.ObjectStore and tracks bytes written/deleted.
type MeteredStore struct {
	store.ObjectStore
	bytesWritten atomic.Int64
}

func (m *MeteredStore) Unwrap() store.ObjectStore { return m.ObjectStore }

func NewMeteredStore(s store.ObjectStore) *MeteredStore {
	return &MeteredStore{ObjectStore: s}
}

func (m *MeteredStore) Delete(ctx context.Context, key string) error {
	_, err := m.DeleteReturnSize(ctx, key)
	return err
}

func (m *MeteredStore) DeleteReturnSize(ctx context.Context, key string) (int64, error) {
	size, err := m.storedSize(ctx, key)
	if err != nil {
		return 0, err
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
	if !strings.HasPrefix(key, "index/") {
		m.bytesWritten.Add(int64(len(data)))
	}
	return nil
}

func (m *MeteredStore) BytesWritten() int64 {
	return m.bytesWritten.Load()
}

func (m *MeteredStore) Reset() {
	m.bytesWritten.Store(0)
}

// DeleteAll implements store.BatchDeleter, metering what it deletes.
func (m *MeteredStore) DeleteAll(ctx context.Context, keys []string) error {
	_, err := m.DeleteAllReturnSizes(ctx, keys)
	return err
}

// DeleteAllReturnSizes deletes keys in as few backend requests as the wrapped
// store allows and returns the stored size of every key it could confirm gone.
//
// **The returned map is the accounting, and it is deliberately not "keys minus
// an error".** A key is in it only when its size was read *and* the store
// confirmed the deletion, because those are the two things that have to be true
// before the meter may credit the space back. Everything else — a size that
// could not be read, a key the backend refused, a batch whose request never
// landed — leaves the key out and appears in the error, and the caller counts
// neither the object nor its bytes.
//
// That is what keeps a batched sweep as honest as the one-key-at-a-time version
// it replaces. DeleteObjects reports success and failure per key in one
// response; reading such a response as a plain error/no-error would either
// credit a thousand objects because most of them worked, or credit none because
// one did not, and a garbage collector that reports space it did not reclaim is
// the more dangerous of those two (docs/compatibility.md).
func (m *MeteredStore) DeleteAllReturnSizes(ctx context.Context, keys []string) (map[string]int64, error) {
	sizes := make(map[string]int64, len(keys))
	sized := make([]string, 0, len(keys))
	var failures store.DeleteErrors

	// A key whose size cannot be read is not deleted at all, matching
	// DeleteReturnSize: deleting bytes the meter cannot count would make the
	// reclaimed total wrong in the direction that overstates it.
	for _, key := range keys {
		size, err := m.storedSize(ctx, key)
		if err != nil {
			failures = append(failures, store.DeleteError{Key: key, Err: err})
			continue
		}
		sizes[key] = size
		sized = append(sized, key)
	}

	if err := store.DeleteAll(ctx, m.ObjectStore, sized); err != nil {
		unconfirmed, ok := store.FailedDeletes(err)
		if !ok {
			// No per-key detail, so nothing in the batch is confirmed gone.
			// Crediting none of it understates the space reclaimed, which is
			// the safe direction; crediting all of it is the unsafe one.
			unconfirmed = store.UnconfirmedDeletes(sized, err)
		}
		failures = append(failures, unconfirmed...)
		for _, key := range unconfirmed.Keys() {
			delete(sizes, key)
		}
	}

	var reclaimed int64
	for _, size := range sizes {
		reclaimed += size
	}
	m.bytesWritten.Add(-reclaimed)

	if len(failures) > 0 {
		return sizes, failures
	}
	return sizes, nil
}

// storedSize is the size the meter credits back for a key. Objects under
// "index/" are not metered on the way in (see Put), so they must not be
// credited on the way out either.
func (m *MeteredStore) storedSize(ctx context.Context, key string) (int64, error) {
	if strings.HasPrefix(key, "index/") {
		return 0, nil
	}
	return m.Size(ctx, key)
}
