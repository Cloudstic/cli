package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// unreadableLockStore serves every key normally except the lock objects, whose
// reads fail while Exists keeps reporting them present. That is a real backend
// state, not a contrived one: an object can be listed and stat-able while a
// GET is throttled, reset, or served a truncated body.
type unreadableLockStore struct {
	*MockStore
	failPrefix string
}

var errLockUnreadable = errors.New("SlowDown: request rate exceeded")

func (u *unreadableLockStore) Get(ctx context.Context, key string) ([]byte, error) {
	if strings.HasPrefix(key, u.failPrefix) {
		return nil, errLockUnreadable
	}
	return u.MockStore.Get(ctx, key)
}

// A lock that cannot be read is a lock. Concluding otherwise is how a prune
// starts alongside a running backup and sweeps the objects that backup has
// written but not yet referenced from a snapshot.
func TestAcquireRepoLockRefusesWhenExclusiveLockIsUnreadable(t *testing.T) {
	ctx := context.Background()
	base := NewMockStore()

	// Someone holds the exclusive lock.
	holder, _, err := AcquireRepoLock(ctx, base, "backup")
	if err != nil {
		t.Fatalf("seed lock: %v", err)
	}
	defer holder.Release()

	blind := &unreadableLockStore{MockStore: base, failPrefix: exclusiveLockKey}

	if _, _, err := AcquireRepoLock(ctx, blind, "prune"); err == nil {
		t.Fatal("prune acquired the exclusive lock while an existing one was merely unreadable")
	}
	if _, _, err := AcquireSharedLock(ctx, blind, "restore"); err == nil {
		t.Fatal("restore acquired a shared lock while the exclusive lock was merely unreadable")
	}
}

// The same reasoning in the other direction: an exclusive operation must not
// step over a shared lock it cannot read.
func TestAcquireRepoLockRefusesWhenSharedLockIsUnreadable(t *testing.T) {
	ctx := context.Background()
	base := NewMockStore()

	holder, _, err := AcquireSharedLock(ctx, base, "backup")
	if err != nil {
		t.Fatalf("seed shared lock: %v", err)
	}
	defer holder.Release()

	blind := &unreadableLockStore{MockStore: base, failPrefix: sharedLockPrefix}

	if _, _, err := AcquireRepoLock(ctx, blind, "prune"); err == nil {
		t.Fatal("prune acquired the exclusive lock while a shared lock was merely unreadable")
	}
}

// The permissive case must stay permissive: a repository with no lock at all
// is not blocked by this. Absence and unreadability are different answers and
// the fix depends on telling them apart, so both directions are pinned.
func TestAcquireRepoLockSucceedsOnUnlockedRepository(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lock, _, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("acquire on an unlocked repository: %v", err)
	}
	lock.Release()
}
