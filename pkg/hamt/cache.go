package hamt

import (
	"encoding/json"
	"fmt"

	"github.com/cloudstic/cli/pkg/core"
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

func (cs *cachingStore) Put(key string, data []byte) error {
	cs.staging[key] = data
	return nil
}

func (cs *cachingStore) Get(key string) ([]byte, error) {
	if data, ok := cs.staging[key]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("key %s not found in cache", key)
}

func (cs *cachingStore) Exists(key string) (bool, error) {
	_, ok := cs.staging[key]
	return ok, nil
}

func (cs *cachingStore) Delete(key string) error {
	delete(cs.staging, key)
	return nil
}

func (cs *cachingStore) List(string) ([]string, error) {
	return nil, fmt.Errorf("list not supported on cache")
}

func (cs *cachingStore) Size(key string) (int64, error) {
	data, ok := cs.staging[key]
	if !ok {
		return 0, fmt.Errorf("key %s not found in cache", key)
	}
	return int64(len(data)), nil
}

func (cs *cachingStore) TotalSize() (int64, error) {
	var total int64
	for _, data := range cs.staging {
		total += int64(len(data))
	}
	return total, nil
}

// TransactionalStore buffers HAMT node writes in memory and flushes only the
// reachable subset to the persistent store. Reads fall through to the
// persistent store when the key is not in the cache.
type TransactionalStore struct {
	cache      *cachingStore
	persistent store.ObjectStore
}

func NewTransactionalStore(persistent store.ObjectStore) *TransactionalStore {
	return &TransactionalStore{
		cache:      newCachingStore(),
		persistent: persistent,
	}
}

func (ts *TransactionalStore) Put(key string, data []byte) error {
	return ts.cache.Put(key, data)
}

func (ts *TransactionalStore) Get(key string) ([]byte, error) {
	if data, err := ts.cache.Get(key); err == nil {
		return data, nil
	}
	return ts.persistent.Get(key)
}

func (ts *TransactionalStore) Exists(key string) (bool, error) {
	if ok, _ := ts.cache.Exists(key); ok {
		return true, nil
	}
	return ts.persistent.Exists(key)
}

func (ts *TransactionalStore) Delete(key string) error {
	return ts.cache.Delete(key)
}

func (ts *TransactionalStore) List(prefix string) ([]string, error) {
	return ts.persistent.List(prefix)
}

func (ts *TransactionalStore) Size(key string) (int64, error) {
	if size, err := ts.cache.Size(key); err == nil {
		return size, nil
	}
	return ts.persistent.Size(key)
}

func (ts *TransactionalStore) TotalSize() (int64, error) {
	return ts.persistent.TotalSize()
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
				errs <- ts.persistent.Put(j.key, j.data)
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
