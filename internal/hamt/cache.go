package hamt

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

// cachingStore is an in-memory write buffer for HAMT nodes. It is not safe for
// concurrent use; all access must be serialised by the caller.
type cachingStore struct {
	staging map[string][]byte
}

func newCachingStore() *cachingStore {
	return &cachingStore{staging: make(map[string][]byte)}
}

func (cs *cachingStore) Put(_ context.Context, key string, data []byte) error {
	cs.staging[key] = data
	return nil
}

func (cs *cachingStore) Get(_ context.Context, key string) ([]byte, error) {
	if data, ok := cs.staging[key]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("key %s not found in cache", key)
}

func (cs *cachingStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := cs.staging[key]
	return ok, nil
}

func (cs *cachingStore) Delete(_ context.Context, key string) error {
	delete(cs.staging, key)
	return nil
}

func (cs *cachingStore) List(_ context.Context, _ string) ([]string, error) {
	return nil, fmt.Errorf("list not supported on cache")
}

func (cs *cachingStore) Size(_ context.Context, key string) (int64, error) {
	data, ok := cs.staging[key]
	if !ok {
		return 0, fmt.Errorf("key %s not found in cache", key)
	}
	return int64(len(data)), nil
}

func (cs *cachingStore) TotalSize(_ context.Context) (int64, error) {
	var total int64
	for _, data := range cs.staging {
		total += int64(len(data))
	}
	return total, nil
}

// TransactionalStore buffers HAMT node writes in memory and flushes only the
// reachable subset to the persistent store.
type TransactionalStore struct {
	cache      *cachingStore
	readCache  map[string][]byte
	persistent store.ObjectStore
}

func NewTransactionalStore(persistent store.ObjectStore) *TransactionalStore {
	return &TransactionalStore{
		cache:      newCachingStore(),
		readCache:  make(map[string][]byte),
		persistent: persistent,
	}
}

func (ts *TransactionalStore) Put(ctx context.Context, key string, data []byte) error {
	return ts.cache.Put(ctx, key, data)
}

func (ts *TransactionalStore) Get(ctx context.Context, key string) ([]byte, error) {
	if data, err := ts.cache.Get(ctx, key); err == nil {
		return data, nil
	}
	if data, ok := ts.readCache[key]; ok {
		return data, nil
	}
	data, err := ts.persistent.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	ts.readCache[key] = data
	return data, nil
}

func (ts *TransactionalStore) Exists(ctx context.Context, key string) (bool, error) {
	if ok, _ := ts.cache.Exists(ctx, key); ok {
		return true, nil
	}
	if _, ok := ts.readCache[key]; ok {
		return true, nil
	}
	return ts.persistent.Exists(ctx, key)
}

func (ts *TransactionalStore) Delete(ctx context.Context, key string) error {
	return ts.cache.Delete(ctx, key)
}

func (ts *TransactionalStore) List(ctx context.Context, prefix string) ([]string, error) {
	return ts.persistent.List(ctx, prefix)
}

func (ts *TransactionalStore) Size(ctx context.Context, key string) (int64, error) {
	if size, err := ts.cache.Size(ctx, key); err == nil {
		return size, nil
	}
	if data, ok := ts.readCache[key]; ok {
		return int64(len(data)), nil
	}
	return ts.persistent.Size(ctx, key)
}

func (ts *TransactionalStore) TotalSize(ctx context.Context) (int64, error) {
	return ts.persistent.TotalSize(ctx)
}

// PreloadReadCache populates the read-through cache from externally loaded data.
// Nodes already present in the staging buffer or read cache are not overwritten.
func (ts *TransactionalStore) PreloadReadCache(data map[string][]byte) {
	for k, v := range data {
		if _, ok := ts.cache.staging[k]; ok {
			continue
		}
		if _, ok := ts.readCache[k]; ok {
			continue
		}
		ts.readCache[k] = v
	}
}

// ExportCaches returns a merged view of all node data currently held in memory
// (both the staging buffer and the read cache). The caller can persist this to
// avoid fetching the same nodes on the next run.
func (ts *TransactionalStore) ExportCaches() map[string][]byte {
	out := make(map[string][]byte, len(ts.readCache)+len(ts.cache.staging))
	for k, v := range ts.readCache {
		out[k] = v
	}
	for k, v := range ts.cache.staging {
		out[k] = v
	}
	return out
}

// Flush writes only the HAMT nodes reachable from rootRef to the persistent
// store, ignoring intermediate nodes that were superseded during the
// transaction. Returns the number of nodes written.
func (ts *TransactionalStore) Flush(rootRef string) error {
	if rootRef == "" {
		return nil
	}

	toWrite, err := ts.collectReachable(rootRef)
	if err != nil {
		return err
	}

	fmt.Printf("Flushing HAMT: %d nodes (reduced from %d generated)\n", len(toWrite), len(ts.cache.staging))

	return ts.writeParallel(toWrite)
}

// collectReachable performs a BFS from rootRef through the cache, collecting
// every staged node reachable from the root. Nodes not in the cache are
// already persistent and don't need writing.
func (ts *TransactionalStore) collectReachable(rootRef string) (map[string][]byte, error) {
	queue := []string{rootRef}
	visited := make(map[string]bool)
	toWrite := make(map[string][]byte)

	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]

		if visited[ref] {
			continue
		}
		visited[ref] = true

		data, ok := ts.cache.staging[ref]
		if !ok {
			continue
		}
		toWrite[ref] = data

		var node core.HAMTNode
		if err := json.Unmarshal(data, &node); err != nil {
			return nil, fmt.Errorf("parse node %s during flush: %w", ref, err)
		}
		if node.Type == core.ObjectTypeInternal {
			queue = append(queue, node.Children...)
		}
	}
	return toWrite, nil
}

func (ts *TransactionalStore) writeParallel(toWrite map[string][]byte) error {
	if len(toWrite) == 0 {
		return nil
	}

	ctx := context.Background()

	type job struct {
		key  string
		data []byte
	}

	jobs := make(chan job, len(toWrite))
	errs := make(chan error, len(toWrite))

	workers := min(20, len(toWrite))
	for range workers {
		go func() {
			for j := range jobs {
				if exists, _ := ts.persistent.Exists(ctx, j.key); exists {
					errs <- nil
					continue
				}
				errs <- ts.persistent.Put(ctx, j.key, j.data)
			}
		}()
	}

	for key, data := range toWrite {
		jobs <- job{key: key, data: data}
	}
	close(jobs)

	for range toWrite {
		if err := <-errs; err != nil {
			return err
		}
	}
	return nil
}
