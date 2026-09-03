package store

import (
	"context"
	"fmt"
)

// SizedKey is one listed object together with its stored size.
type SizedKey struct {
	Key  string
	Size int64
}

// KeysOf returns the keys of objects, in order.
func KeysOf(objects []SizedKey) []string {
	keys := make([]string, len(objects))
	for i, o := range objects {
		keys[i] = o.Key
	}
	return keys
}

// SizedLister is an optional interface for backends whose listing can report
// each object's size as it goes.
//
// Every object store this tool targets already returns the size next to the
// key in the same listing response — S3's ListObjectsV2, B2's
// b2_list_file_names, the stat a directory walk makes anyway — and a caller
// that needs both, such as prune's sweep, otherwise pays one round trip per
// key to ask again for what the listing just said. Measured on a
// 20,000-file format-v3 repository, 1,124 of the 1,428 requests a
// `forget --prune` made were Size calls on objects it had listed moments
// before.
//
// fn is called once per object, in listing order; an error it returns stops
// the listing and is returned. The size reported must be what Size returns
// for the key. Backends that cannot do this simply do not implement it, and
// callers go through the ListSized helper, which lists and then sizes each
// key — correct everywhere and merely slower.
type SizedLister interface {
	ListSized(ctx context.Context, prefix string, fn func(key string, size int64) error) error
}

// ListSized lists the keys under prefix with their sizes, through s's
// SizedLister when it has one and with one Size call per listed key when it
// does not.
//
// The capability is looked up on s itself and never by unwrapping, for the
// reason DeleteAll gives: a layer between the caller and the backend may know
// objects the backend does not — PackStore's catalog — or report a different
// size for them, so reaching past it would list the wrong set. Each wrapper
// that can forward safely declares the method itself.
//
// In the fallback, a key whose size cannot be read fails the listing rather
// than being skipped. A caller lists with sizes in order to account for what
// it is about to act on, and a set it could not fully size is one it must not
// act on; failing before acting is the safe order (docs/compatibility.md).
func ListSized(ctx context.Context, s ObjectStore, prefix string, fn func(key string, size int64) error) error {
	if sl, ok := s.(SizedLister); ok {
		return sl.ListSized(ctx, prefix, fn)
	}
	keys, err := s.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		size, err := s.Size(ctx, key)
		if err != nil {
			return fmt.Errorf("size %s: %w", key, err)
		}
		if err := fn(key, size); err != nil {
			return err
		}
	}
	return nil
}

// SizedBatchDeleter is an optional interface for wrappers that account for
// what they delete and can take the sizes from a listing instead of asking the
// store for each key.
//
// It exists for the layers that meter. A prune meters its sweep through one
// MeteredStore, and the client meters everything through another further down
// the chain; if a listing's sizes reached only the outer one, the inner one
// would still ask the store for every key, and the sweep would pay the round
// trips the listing was meant to save. So sizes travel down the chain with the
// keys, and every wrapper that forwards DeleteAll forwards this as well.
//
// Where a size came from changes nothing about what may be credited: a key
// counts only when its size is known and the store confirmed the deletion,
// and per-key failures are reported as DeleteErrors exactly as DeleteAll
// reports them. A listing supplies sizes before deletion, which is the order
// the rule needs — the meter knows what it will credit before the store can
// no longer say.
type SizedBatchDeleter interface {
	DeleteAllSized(ctx context.Context, objects []SizedKey) error
}

// DeleteAllSized deletes objects through s, handing their sizes down when s
// can use them and falling back to DeleteAll with the keys alone when it
// cannot. Like DeleteAll, it looks the capability up on s itself and never
// by unwrapping.
func DeleteAllSized(ctx context.Context, s ObjectStore, objects []SizedKey) error {
	if sd, ok := s.(SizedBatchDeleter); ok {
		return sd.DeleteAllSized(ctx, objects)
	}
	return DeleteAll(ctx, s, KeysOf(objects))
}
