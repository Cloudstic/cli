// Package storetest provides test doubles for store.ObjectStore.
package storetest

import (
	"context"
	"sync"
)

// ObjectStore mirrors the method set of store.ObjectStore. It is declared
// independently, rather than imported, so this package can also be used from
// pkg/store's own internal (package store) tests without an import cycle —
// any store.ObjectStore implementation satisfies this interface structurally.
type ObjectStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Size(ctx context.Context, key string) (int64, error)
	TotalSize(ctx context.Context) (int64, error)
	Flush(ctx context.Context) error
}

// FaultHook decides whether a FaultStore call should fail. key is the object
// key (List passes its prefix instead), and callNum is the 1-indexed count of
// calls to that method so far, including the current one. A nil return lets
// the call proceed to the wrapped store.
type FaultHook func(key string, callNum int) error

// FaultStore wraps an ObjectStore and lets tests inject failures into Get,
// Put, List, or Delete independently of what the backend itself would do —
// simulating a network blip, a permission error, or a partial outage. Each
// hook is optional; a nil hook means that method always passes through.
//
// FaultStore is safe for concurrent use.
type FaultStore struct {
	ObjectStore

	FailGet    FaultHook
	FailPut    FaultHook
	FailList   FaultHook
	FailDelete FaultHook

	mu    sync.Mutex
	calls map[string]int
}

func (f *FaultStore) nextCall(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[method]++
	return f.calls[method]
}

func (f *FaultStore) Get(ctx context.Context, key string) ([]byte, error) {
	if f.FailGet != nil {
		if err := f.FailGet(key, f.nextCall("Get")); err != nil {
			return nil, err
		}
	}
	return f.ObjectStore.Get(ctx, key)
}

func (f *FaultStore) Put(ctx context.Context, key string, data []byte) error {
	if f.FailPut != nil {
		if err := f.FailPut(key, f.nextCall("Put")); err != nil {
			return err
		}
	}
	return f.ObjectStore.Put(ctx, key, data)
}

// Delete honours FailDelete. FaultStore deliberately does not implement
// store.BatchDeleter: this package cannot import pkg/store (pkg/store's own
// internal tests import it), so it could not return the store.DeleteErrors a
// batch caller reads per-key failures out of. Leaving the capability
// unimplemented sends store.DeleteAll down its loop instead, which reports
// these injected failures per key and in the right type.
func (f *FaultStore) Delete(ctx context.Context, key string) error {
	if f.FailDelete != nil {
		if err := f.FailDelete(key, f.nextCall("Delete")); err != nil {
			return err
		}
	}
	return f.ObjectStore.Delete(ctx, key)
}

func (f *FaultStore) List(ctx context.Context, prefix string) ([]string, error) {
	if f.FailList != nil {
		if err := f.FailList(prefix, f.nextCall("List")); err != nil {
			return nil, err
		}
	}
	return f.ObjectStore.List(ctx, prefix)
}

// FailKeys returns a FaultHook that fails every call whose key is in keys,
// regardless of call count. For List, "key" is the queried prefix.
func FailKeys(err error, keys ...string) FaultHook {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return func(key string, _ int) error {
		if set[key] {
			return err
		}
		return nil
	}
}

// FailNth returns a FaultHook that fails only on the nth call (1-indexed) to
// that method, regardless of key. Use it to fail one call in the middle of a
// multi-step operation and assert the rest of the operation reacts correctly.
func FailNth(n int, err error) FaultHook {
	return func(_ string, callNum int) error {
		if callNum == n {
			return err
		}
		return nil
	}
}

// AlwaysFail returns a FaultHook that fails every call to that method.
func AlwaysFail(err error) FaultHook {
	return func(_ string, _ int) error {
		return err
	}
}
