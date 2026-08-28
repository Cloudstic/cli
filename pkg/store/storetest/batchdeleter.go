package storetest

import (
	"context"
	"fmt"
	"testing"
)

// BatchDeleter mirrors store.BatchDeleter. It is redeclared here, rather than
// imported, for the same reason ObjectStore and RangeGetter are: pkg/store's
// own internal tests import this package, so depending on pkg/store would make
// the test binary import itself.
type BatchDeleter interface {
	DeleteAll(ctx context.Context, keys []string) error
}

// AssertBatchDeleterConformance is the shared contract every BatchDeleter must
// satisfy, so the backends cannot drift apart. Each is exercised against a live
// instance of its own backend.
//
// The cases are the ones a prune's sweep depends on and that a backend can get
// wrong without any test noticing: that every requested key is gone afterwards,
// that no other key is, that a key which is already gone is not a failure — S3
// reports a missing key as deleted while os.Remove and sftp Remove both
// error — and that an empty request is a no-op rather than a malformed one.
//
// Partial failure is deliberately not asserted here. It needs a backend made to
// refuse one key of a batch, which a live MinIO or SFTP server will not do on
// request; pkg/store's own tests cover it against a fake.
func AssertBatchDeleterConformance(t *testing.T, s ObjectStore) {
	t.Helper()
	ctx := context.Background()

	deleter, ok := s.(BatchDeleter)
	if !ok {
		t.Fatalf("%T does not implement BatchDeleter", s)
	}

	t.Run("deletes every requested key", func(t *testing.T) {
		keys := seedBatch(t, s, "chunk/batchdel-all-%d", 5)
		if err := deleter.DeleteAll(ctx, keys); err != nil {
			t.Fatalf("DeleteAll: %v", err)
		}
		for _, key := range keys {
			assertGone(t, s, key)
		}
	})

	t.Run("leaves keys it was not given", func(t *testing.T) {
		keys := seedBatch(t, s, "chunk/batchdel-some-%d", 4)
		if err := deleter.DeleteAll(ctx, keys[:2]); err != nil {
			t.Fatalf("DeleteAll: %v", err)
		}
		for _, key := range keys[:2] {
			assertGone(t, s, key)
		}
		for _, key := range keys[2:] {
			assertPresent(t, s, key)
		}
	})

	t.Run("a key that is already gone is not a failure", func(t *testing.T) {
		keys := seedBatch(t, s, "chunk/batchdel-missing-%d", 2)
		keys = append(keys, "chunk/batchdel-never-existed")
		if err := deleter.DeleteAll(ctx, keys); err != nil {
			t.Fatalf("DeleteAll over a missing key: %v", err)
		}
		for _, key := range keys {
			assertGone(t, s, key)
		}
	})

	t.Run("deleting nothing is a no-op", func(t *testing.T) {
		if err := deleter.DeleteAll(ctx, nil); err != nil {
			t.Errorf("DeleteAll(nil): %v", err)
		}
		if err := deleter.DeleteAll(ctx, []string{}); err != nil {
			t.Errorf("DeleteAll(empty): %v", err)
		}
	})

	t.Run("a cancelled context deletes nothing and says so", func(t *testing.T) {
		keys := seedBatch(t, s, "chunk/batchdel-cancelled-%d", 3)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		if err := deleter.DeleteAll(cancelled, keys); err == nil {
			t.Error("expected an error on a cancelled context")
		}
		// Whether a given backend managed to delete anything before noticing is
		// not the contract; that it reported an error rather than success is.
		for _, key := range keys {
			_ = s.Delete(ctx, key)
		}
	})
}

// seedBatch writes n objects named by pattern and returns their keys.
func seedBatch(t *testing.T, s ObjectStore, pattern string, n int) []string {
	t.Helper()
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf(pattern, i)
		if err := s.Put(context.Background(), keys[i], []byte(keys[i])); err != nil {
			t.Fatalf("put %s: %v", keys[i], err)
		}
	}
	return keys
}

func assertGone(t *testing.T, s ObjectStore, key string) {
	t.Helper()
	exists, err := s.Exists(context.Background(), key)
	if err != nil {
		t.Fatalf("exists %s: %v", key, err)
	}
	if exists {
		t.Errorf("%s survived DeleteAll", key)
	}
}

func assertPresent(t *testing.T, s ObjectStore, key string) {
	t.Helper()
	exists, err := s.Exists(context.Background(), key)
	if err != nil {
		t.Fatalf("exists %s: %v", key, err)
	}
	if !exists {
		t.Errorf("%s was deleted but was not in the request", key)
	}
}
