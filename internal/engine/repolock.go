package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cloudstic/cli/pkg/store"
)

// The lock path must be distinct from the shared lock directory on local filesystems.
// We use index/lock.exclusive for the exclusive lock and index/lock.shared/ for shared locks.
const (
	exclusiveLockKey = "index/lock.exclusive"
	sharedLockPrefix = "index/lock.shared/"
)

var (
	lockTTL     = 1 * time.Minute
	refreshRate = 30 * time.Second
)

// RepoLock is the JSON payload stored at index/lock (or index/lock/shared/<uuid>).
type RepoLock struct {
	Operation  string `json:"operation"`
	Holder     string `json:"holder"`
	AcquiredAt string `json:"acquired_at"`
	ExpiresAt  string `json:"expires_at"`
	IsShared   bool   `json:"is_shared,omitempty"`
}

// ownsLock returns true when current matches the holder identity of ref.
func (ref *RepoLock) ownsLock(current *RepoLock) bool {
	return current.Holder == ref.Holder && current.AcquiredAt == ref.AcquiredAt
}

// LockHandle is returned by AcquireRepoLock and must be released when the
// operation completes. A background goroutine refreshes the lock every
// refreshRate so that the TTL stays short (fast recovery on crash) while
// supporting arbitrarily long operations.
//
// cancel cancels the operation context returned alongside the handle. It is
// called by Release on normal completion, and by the refresh goroutine the
// moment it can no longer prove ownership of the lock — so an operation that
// has lost its lock (TTL expired after a sleep, or another machine took over)
// is aborted rather than left writing into a repository someone else now owns.
type LockHandle struct {
	store  store.ObjectStore
	key    string
	lock   RepoLock
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Release stops the refresh goroutine and deletes the lock only if this
// handle still owns it (prevents deleting a lock acquired by another process
// after ours expired).
func (h *LockHandle) Release() {
	h.cancel()
	h.wg.Wait()
	ctx := context.Background()

	current, err := readLockByKey(ctx, h.store, h.key)
	if err != nil {
		return
	}
	if h.lock.ownsLock(current) {
		_ = h.store.Delete(ctx, h.key)
	}
}

// AcquireRepoLock creates an exclusive lock for operation. If another
// non-expired lock exists (exclusive or shared), the call returns an error.
//
// The returned context is derived from ctx and is cancelled if the lock is
// ever lost while held, so the caller must run the operation under it rather
// than under the original ctx.
//
// To mitigate TOCTOU races on stores without conditional writes, the lock is
// written and then immediately re-read to verify this process still owns it.
func AcquireRepoLock(ctx context.Context, s store.ObjectStore, operation string) (*LockHandle, context.Context, error) {
	if err := checkExclusiveLock(ctx, s); err != nil {
		return nil, nil, err
	}

	// Check if any shared locks are active.
	if err := checkSharedLocks(ctx, s); err != nil {
		return nil, nil, fmt.Errorf("cannot acquire exclusive lock: %w", err)
	}

	holder := lockHolder()
	now := time.Now()
	lock := RepoLock{
		Operation:  operation,
		Holder:     holder,
		AcquiredAt: now.Format(time.RFC3339Nano),
		ExpiresAt:  now.Add(lockTTL).Format(time.RFC3339Nano),
	}
	if err := writeLockByKey(ctx, s, exclusiveLockKey, lock); err != nil {
		return nil, nil, fmt.Errorf("acquire repo lock: %w", err)
	}

	// Re-read to verify ownership (TOCTOU mitigation).
	current, err := readLockByKey(ctx, s, exclusiveLockKey)
	if err != nil {
		return nil, nil, fmt.Errorf("verify repo lock: %w", err)
	}
	if !lock.ownsLock(current) {
		return nil, nil, fmt.Errorf(
			"repository is locked by %s (operation: %s) — lost lock race",
			current.Holder, current.Operation,
		)
	}

	// Re-check shared locks after writing ours, mirroring the exclusive re-read
	// AcquireSharedLock performs. Without this, a shared lock written between
	// our initial checkSharedLocks and our write would go unseen and both the
	// exclusive operation and the shared one would proceed concurrently.
	if err := checkSharedLocks(ctx, s); err != nil {
		// Roll back only if we still own the exclusive lock, so a concurrent
		// exclusive acquirer that overwrote our key is not deleted from under it.
		if owner, readErr := readLockByKey(ctx, s, exclusiveLockKey); readErr == nil && lock.ownsLock(owner) {
			_ = s.Delete(ctx, exclusiveLockKey)
		}
		return nil, nil, fmt.Errorf("cannot acquire exclusive lock: %w", err)
	}

	opCtx, cancel := context.WithCancel(ctx)
	h := &LockHandle{store: s, key: exclusiveLockKey, lock: lock, cancel: cancel}
	h.wg.Add(1)
	go h.refreshLoop(opCtx)

	return h, opCtx, nil
}

// AcquireSharedLock creates a shared lock for an operation (like backup or restore).
// If an exclusive lock exists, the call returns an error.
// Multiple shared locks can exist simultaneously.
//
// The returned context is derived from ctx and is cancelled if the lock is
// ever lost while held, so the caller must run the operation under it rather
// than under the original ctx.
func AcquireSharedLock(ctx context.Context, s store.ObjectStore, operation string) (*LockHandle, context.Context, error) {
	// 1. Check if an exclusive lock exists
	if err := checkExclusiveLock(ctx, s); err != nil {
		return nil, nil, err
	}

	// 2. Write our shared lock
	holder := lockHolder()
	now := time.Now()
	sharedLockKey, err := newSharedLockKey()
	if err != nil {
		return nil, nil, err
	}

	lock := RepoLock{
		Operation:  operation,
		Holder:     holder,
		AcquiredAt: now.Format(time.RFC3339Nano),
		ExpiresAt:  now.Add(lockTTL).Format(time.RFC3339Nano),
		IsShared:   true,
	}
	if err := writeLockByKey(ctx, s, sharedLockKey, lock); err != nil {
		return nil, nil, fmt.Errorf("acquire shared lock: %w", err)
	}

	// 3. Re-read exclusive lock to catch race with prune creating an exclusive lock at the same time
	if err := checkExclusiveLock(ctx, s); err != nil {
		// An exclusive lock appeared while we were writing ours. Roll back.
		_ = s.Delete(ctx, sharedLockKey)
		return nil, nil, fmt.Errorf("while acquiring shared lock: %w", err)
	}

	opCtx, cancel := context.WithCancel(ctx)
	h := &LockHandle{store: s, key: sharedLockKey, lock: lock, cancel: cancel}
	h.wg.Add(1)
	go h.refreshLoop(opCtx)

	return h, opCtx, nil
}

func newSharedLockKey() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate shared lock key: %w", err)
	}
	return fmt.Sprintf("%s%x", sharedLockPrefix, nonce), nil
}

const maxRefreshFailures = 3

func (h *LockHandle) refreshLoop(ctx context.Context) {
	defer h.wg.Done()
	ticker := time.NewTicker(refreshRate)
	defer ticker.Stop()

	consecutiveFailures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := readLockByKey(ctx, h.store, h.key)
			if err != nil {
				consecutiveFailures++
				if consecutiveFailures >= maxRefreshFailures {
					// Cannot confirm we still hold the lock; the TTL will lapse
					// and another process may take over. Abort the operation.
					h.cancel()
					return
				}
				continue
			}
			if !h.lock.ownsLock(current) {
				// Another holder owns the lock now. Abort the operation before
				// it writes into a repository we no longer control.
				h.cancel()
				return
			}
			h.lock.ExpiresAt = time.Now().Add(lockTTL).Format(time.RFC3339Nano)
			if err := writeLockByKey(ctx, h.store, h.key, h.lock); err != nil {
				consecutiveFailures++
				if consecutiveFailures >= maxRefreshFailures {
					h.cancel()
					return
				}
				continue
			}
			consecutiveFailures = 0
		}
	}
}

// CheckRepoLock returns an error if the repository is currently locked by
// another operation (either exclusive or shared). Callers that only need to detect conflicts
// use this instead of acquiring their own lock.
func CheckRepoLock(ctx context.Context, s store.ObjectStore) error {
	if err := checkExclusiveLock(ctx, s); err != nil {
		return err
	}
	return checkSharedLocks(ctx, s)
}

// checkExclusiveLock returns nil only when it can show no live exclusive lock
// exists, and an error in every other case — including the cases where it
// cannot tell.
//
// The distinction is the whole point. A lock read can fail three ways that look
// alike from the call site and are not alike at all: the object is absent (safe
// to proceed), the object is present but could not be fetched (a live lock we
// must respect), or the store itself is unreachable (we know nothing). Treating
// the last two as "no lock" is what lets a prune start while a backup is
// running and delete the objects that backup is still writing — a transient
// network error becoming data loss.
//
// An unparseable expiry is treated the same way, as still locked. A lock we
// cannot read the clock off is a lock, and the TTL will retire it soon enough
// if the holder really is gone.
func checkExclusiveLock(ctx context.Context, s store.ObjectStore) error {
	existing, err := readLockByKey(ctx, s, exclusiveLockKey)
	if err != nil {
		exists, existsErr := s.Exists(ctx, exclusiveLockKey)
		if existsErr != nil {
			return fmt.Errorf("cannot determine whether the repository is locked: %w", existsErr)
		}
		if exists {
			return fmt.Errorf("repository holds an unreadable exclusive lock: %w", err)
		}
		return nil
	}

	expires, parseErr := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
	if parseErr != nil || time.Now().Before(expires) {
		return fmt.Errorf(
			"repository is exclusively locked by %s (operation: %s, acquired: %s, expires: %s)",
			existing.Holder, existing.Operation, existing.AcquiredAt, existing.ExpiresAt,
		)
	}
	return nil
}

// BreakRepoLock forcibly removes the repository lock (exclusive and shared)
// regardless of who holds it. Returns the locks that were removed.
func BreakRepoLock(ctx context.Context, s store.ObjectStore) ([]*RepoLock, error) {
	var removed []*RepoLock

	existing, err := readLockByKey(ctx, s, exclusiveLockKey)
	if err == nil {
		removed = append(removed, existing)
	}

	_ = s.Delete(ctx, exclusiveLockKey)

	keys, listErr := s.List(ctx, sharedLockPrefix)
	if listErr == nil {
		for _, key := range keys {
			sharedLock, err := readLockByKey(ctx, s, key)
			if err == nil {
				removed = append(removed, sharedLock)
			}
			_ = s.Delete(ctx, key)
		}
	}

	return removed, nil
}

func checkSharedLocks(ctx context.Context, s store.ObjectStore) error {
	keys, err := s.List(ctx, sharedLockPrefix)
	if err != nil {
		return err // Might return not found or un-implemented if store is basic, but ObjectStore has List
	}

	for _, key := range keys {
		sharedLock, err := readLockByKey(ctx, s, key)
		if err != nil {
			// The key was listed a moment ago, so "gone now" is the likely
			// explanation and is safe to skip. "Still there but unreadable" is
			// not: it is a live shared lock, and stepping over it lets an
			// exclusive operation run against a repository someone is reading.
			exists, existsErr := s.Exists(ctx, key)
			if existsErr != nil {
				return fmt.Errorf("cannot determine whether shared lock %s is live: %w", key, existsErr)
			}
			if exists {
				return fmt.Errorf("repository holds an unreadable shared lock %s: %w", key, err)
			}
			continue
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, sharedLock.ExpiresAt)
		if parseErr != nil || time.Now().Before(expires) {
			return fmt.Errorf(
				"repository is locked (shared) by %s (operation: %s)",
				sharedLock.Holder, sharedLock.Operation,
			)
		}
	}
	return nil
}

func readLockByKey(ctx context.Context, s store.ObjectStore, key string) (*RepoLock, error) {
	data, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	var lock RepoLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	return &lock, nil
}

func writeLockByKey(ctx context.Context, s store.ObjectStore, key string, lock RepoLock) error {
	data, err := json.Marshal(lock)
	if err != nil {
		return err
	}
	return s.Put(ctx, key, data)
}

func lockHolder() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s (pid %d)", host, os.Getpid())
}
