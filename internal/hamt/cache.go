package hamt

import (
	"context"
	"fmt"

	"github.com/buger/jsonparser"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

var log = logger.New("hamt", logger.ColorCyan)

const readCacheSize = 4096

// TransactionalStore buffers HAMT node writes in memory and flushes only the
// reachable subset to the persistent store.
type TransactionalStore struct {
	staging    map[string][]byte
	readCache  *lru.Cache[string, []byte]
	persistent store.ObjectStore
}

func NewTransactionalStore(persistent store.ObjectStore) *TransactionalStore {
	rc, _ := lru.New[string, []byte](readCacheSize)
	return &TransactionalStore{
		staging:    make(map[string][]byte),
		readCache:  rc,
		persistent: persistent,
	}
}

func (ts *TransactionalStore) Put(_ context.Context, key string, data []byte) error {
	ts.staging[key] = data
	return nil
}

func (ts *TransactionalStore) Get(ctx context.Context, key string) ([]byte, error) {
	if data, ok := ts.staging[key]; ok {
		log.Debugf("get %s: hit staging (%d bytes)", key, len(data))
		return data, nil
	}
	if data, ok := ts.readCache.Get(key); ok {
		log.Debugf("get %s: hit read cache (%d bytes)", key, len(data))
		return data, nil
	}
	data, err := ts.persistent.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	log.Debugf("get %s: fetched from persistent (%d bytes, cache size %d/%d)", key, len(data), ts.readCache.Len(), readCacheSize)
	ts.readCache.Add(key, data)
	return data, nil
}

func (ts *TransactionalStore) Exists(ctx context.Context, key string) (bool, error) {
	if _, ok := ts.staging[key]; ok {
		return true, nil
	}
	if ts.readCache.Contains(key) {
		return true, nil
	}
	return ts.persistent.Exists(ctx, key)
}

func (ts *TransactionalStore) Delete(_ context.Context, key string) error {
	delete(ts.staging, key)
	return nil
}

func (ts *TransactionalStore) List(ctx context.Context, prefix string) ([]string, error) {
	return ts.persistent.List(ctx, prefix)
}

func (ts *TransactionalStore) Size(ctx context.Context, key string) (int64, error) {
	if data, ok := ts.staging[key]; ok {
		return int64(len(data)), nil
	}
	if data, ok := ts.readCache.Get(key); ok {
		return int64(len(data)), nil
	}
	return ts.persistent.Size(ctx, key)
}

func (ts *TransactionalStore) TotalSize(ctx context.Context) (int64, error) {
	return ts.persistent.TotalSize(ctx)
}

// FlushReachable writes only the HAMT nodes reachable from rootRef to the persistent
// store, ignoring intermediate nodes that were superseded during the
// transaction.
func (ts *TransactionalStore) FlushReachable(rootRef string) error {
	if rootRef == "" {
		return nil
	}

	stagedCount := len(ts.staging)

	toWrite, err := ts.collectReachable(rootRef)
	if err != nil {
		return err
	}

	// Discard unreachable nodes from the staging buffer.
	ts.staging = toWrite

	discarded := stagedCount - len(toWrite)
	fmt.Printf("Flushing HAMT: %d reachable nodes (%d intermediate discarded)\n", len(toWrite), discarded)
	log.Debugf("flush: staged=%d reachable=%d discarded=%d root=%s", stagedCount, len(toWrite), discarded, rootRef)

	return ts.writeParallel(toWrite)
}

// collectReachable performs a BFS from rootRef through the staging buffer,
// collecting every staged node reachable from the root. Nodes not in staging
// are already persistent and don't need writing.
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

		data, ok := ts.staging[ref]
		if !ok {
			continue
		}
		toWrite[ref] = data

		nodeType, err := jsonparser.GetString(data, "type")
		if err != nil {
			return nil, fmt.Errorf("parse node type %s during flush: %w", ref, err)
		}
		if core.ObjectType(nodeType) == core.ObjectTypeInternal {
			_, err = jsonparser.ArrayEach(data, func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
				if dataType == jsonparser.String {
					str, _ := jsonparser.ParseString(value)
					queue = append(queue, str)
				}
			}, "children")
			if err != nil && err != jsonparser.KeyPathNotFoundError {
				return nil, fmt.Errorf("parse node children %s during flush: %w", ref, err)
			}
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

	var totalBytes int
	for _, data := range toWrite {
		totalBytes += len(data)
	}
	log.Debugf("writeParallel: writing %d nodes (%d bytes total)", len(toWrite), totalBytes)

	jobs := make(chan job, len(toWrite))
	errs := make(chan error, len(toWrite))

	workers := min(store.GetConcurrencyHint(ts.persistent, 20), len(toWrite))
	for range workers {
		go func() {
			for j := range jobs {
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

func (ts *TransactionalStore) Flush(ctx context.Context) error {
	return ts.persistent.Flush(ctx)
}
