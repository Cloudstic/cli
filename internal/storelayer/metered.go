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

// GetRange implements store.RangeGetter. Metering counts bytes written and has
// nothing to say about how a read is served, so a ranged read passes straight
// through — and it has to, or a v3 repository's blob reads would each transfer
// a whole blob to reach one member, which is the exact cost the byte range
// exists to avoid.
//
// Declaring the method makes MeteredStore satisfy RangeGetter unconditionally,
// including over an inner store that cannot range, so the fallback is explicit
// rather than inherited. Embedding store.ObjectStore does not do this: the
// embedded interface's method set is the only thing promoted, so a wrapper
// silently loses every optional capability its inner store had.
func (m *MeteredStore) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	return store.GetRange(ctx, m.ObjectStore, key, offset, length)
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

// DeleteAllSized implements store.SizedBatchDeleter, metering what it deletes
// from the sizes it was handed. See DeleteListed.
func (m *MeteredStore) DeleteAllSized(ctx context.Context, objects []store.SizedKey) error {
	_, err := m.DeleteListed(ctx, objects)
	return err
}

// ListSized implements store.SizedLister by forwarding. Metering counts bytes
// written and has nothing to say about a listing — but the method has to be
// declared, or the capability stops at this layer and a sweep above it pays a
// Size per listed key (the GetRange trap, again).
func (m *MeteredStore) ListSized(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	return store.ListSized(ctx, m.ObjectStore, prefix, fn)
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
//
// It reads each key's size from the store, one request per key on most
// backends. A caller that has just listed the keys already holds their sizes
// and should hand them to DeleteListed instead, which applies the same rule
// without the round trips.
func (m *MeteredStore) DeleteAllReturnSizes(ctx context.Context, keys []string) (map[string]int64, error) {
	sizes := make(map[string]int64, len(keys))
	sized := make([]store.SizedKey, 0, len(keys))
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
		sized = append(sized, store.SizedKey{Key: key, Size: size})
	}
	return m.deleteSized(ctx, sized, sizes, failures)
}

// DeleteListed deletes objects a sized listing produced and returns the size of
// every one it could confirm gone, under exactly the rule DeleteAllReturnSizes
// states: a key is credited only when its size is known and the store
// confirmed the deletion. The listing is where the size was read — before the
// deletion, as the rule requires, since a meter must know what it will credit
// while the store can still say — and so no key costs a request of its own.
// That is the whole point: a sweep sized every object it had just listed, at
// one round trip each, for the reclaimed-bytes figure alone.
//
// The sizes are also passed down the chain (store.DeleteAllSized), so a
// second meter beneath this one credits from the same listing instead of
// asking the store again.
func (m *MeteredStore) DeleteListed(ctx context.Context, objects []store.SizedKey) (map[string]int64, error) {
	sizes := make(map[string]int64, len(objects))
	for _, o := range objects {
		sizes[o.Key] = m.creditable(o.Key, o.Size)
	}
	return m.deleteSized(ctx, objects, sizes, nil)
}

// deleteSized is the accounting DeleteAllReturnSizes and DeleteListed share.
// objects are the keys to delete with their sizes; sizes is the credit each
// would earn if confirmed gone (which differs from the stored size under
// "index/", see creditable); failures is what the caller has already ruled out.
func (m *MeteredStore) deleteSized(ctx context.Context, objects []store.SizedKey, sizes map[string]int64, failures store.DeleteErrors) (map[string]int64, error) {
	if err := store.DeleteAllSized(ctx, m.ObjectStore, objects); err != nil {
		unconfirmed, ok := store.FailedDeletes(err)
		if !ok {
			// No per-key detail, so nothing in the batch is confirmed gone.
			// Crediting none of it understates the space reclaimed, which is
			// the safe direction; crediting all of it is the unsafe one.
			unconfirmed = store.UnconfirmedDeletes(store.KeysOf(objects), err)
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

// creditable is what the meter credits back for a key of a known stored size.
// Objects under "index/" are not metered on the way in (see Put), so they must
// not be credited on the way out either.
func (m *MeteredStore) creditable(key string, size int64) int64 {
	if strings.HasPrefix(key, "index/") {
		return 0
	}
	return size
}

// storedSize is creditable for a key whose size has to be asked for, skipping
// the request where the answer would not be credited anyway.
func (m *MeteredStore) storedSize(ctx context.Context, key string) (int64, error) {
	if strings.HasPrefix(key, "index/") {
		return 0, nil
	}
	return m.Size(ctx, key)
}
