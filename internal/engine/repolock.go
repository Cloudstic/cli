package engine

import (
	"context"
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
type LockHandle struct {
	store  store.ObjectStore
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

	key := exclusiveLockKey
	if h.lock.IsShared {
		// Use the specific shared lock key for releasing
		key = fmt.Sprintf("%s%s", sharedLockPrefix, h.lock.AcquiredAt) // or we could store the key in LockHandle
	}

	current, err := readLockByKey(ctx, h.store, key)
	if err != nil {
		return
	}
	if h.lock.ownsLock(current) {
		_ = h.store.Delete(ctx, key)
	}
}

// AcquireRepoLock creates an exclusive lock for operation. If another
// non-expired lock exists (exclusive or shared), the call returns an error.
//
// To mitigate TOCTOU races on stores without conditional writes, the lock is
// written and then immediately re-read to verify this process still owns it.
func AcquireRepoLock(ctx context.Context, s store.ObjectStore, operation string) (*LockHandle, error) {
	if existing, err := readLockByKey(ctx, s, exclusiveLockKey); err == nil {
		expires, parseErr := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
		if parseErr != nil || time.Now().Before(expires) {
			return nil, fmt.Errorf(
				"repository is locked by %s (operation: %s, acquired: %s, expires: %s)",
				existing.Holder, existing.Operation, existing.AcquiredAt, existing.ExpiresAt,
			)
		}
	}

	// Check if any shared locks are active.
	if err := checkSharedLocks(ctx, s); err != nil {
		return nil, fmt.Errorf("cannot acquire exclusive lock: %w", err)
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
		return nil, fmt.Errorf("acquire repo lock: %w", err)
	}

	// Re-read to verify ownership (TOCTOU mitigation).
	current, err := readLockByKey(ctx, s, exclusiveLockKey)
	if err != nil {
		return nil, fmt.Errorf("verify repo lock: %w", err)
	}
	if !lock.ownsLock(current) {
		return nil, fmt.Errorf(
			"repository is locked by %s (operation: %s) — lost lock race",
			current.Holder, current.Operation,
		)
	}

	refreshCtx, cancel := context.WithCancel(ctx)
	h := &LockHandle{store: s, lock: lock, cancel: cancel}
	h.wg.Add(1)
	go h.refreshLoop(refreshCtx)

	return h, nil
}

// AcquireSharedLock creates a shared lock for an operation (like backup or restore).
// If an exclusive lock exists, the call returns an error.
// Multiple shared locks can exist simultaneously.
func AcquireSharedLock(ctx context.Context, s store.ObjectStore, operation string) (*LockHandle, error) {
	// 1. Check if an exclusive lock exists
	if existing, err := readLockByKey(ctx, s, exclusiveLockKey); err == nil {
		expires, parseErr := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
		if parseErr != nil || time.Now().Before(expires) {
			return nil, fmt.Errorf(
				"repository is exclusively locked by %s (operation: %s)",
				existing.Holder, existing.Operation,
			)
		}
	}

	// 2. Write our shared lock
	holder := lockHolder()
	now := time.Now()
	// Use crypto rand or just timestamp+pid for uniqueness
	sharedLockKey := fmt.Sprintf("%s%s", sharedLockPrefix, now.Format(time.RFC3339Nano))

	lock := RepoLock{
		Operation:  operation,
		Holder:     holder,
		AcquiredAt: now.Format(time.RFC3339Nano),
		ExpiresAt:  now.Add(lockTTL).Format(time.RFC3339Nano),
		IsShared:   true,
	}
	if err := writeLockByKey(ctx, s, sharedLockKey, lock); err != nil {
		return nil, fmt.Errorf("acquire shared lock: %w", err)
	}

	// 3. Re-read exclusive lock to catch race with prune creating an exclusive lock at the same time
	if existing, err := readLockByKey(ctx, s, exclusiveLockKey); err == nil {
		expires, parseErr := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
		if parseErr != nil || time.Now().Before(expires) {
			// Found an exclusive lock that was created during our shared lock creation
			// Rollback our shared lock
			_ = s.Delete(ctx, sharedLockKey)
			return nil, fmt.Errorf(
				"repository was exclusively locked by %s while acquiring shared lock",
				existing.Holder,
			)
		}
	}

	refreshCtx, cancel := context.WithCancel(ctx)
	h := &LockHandle{store: s, lock: lock, cancel: cancel}
	h.wg.Add(1)
	go h.refreshLoop(refreshCtx)

	return h, nil
}

const maxRefreshFailures = 3

func (h *LockHandle) refreshLoop(ctx context.Context) {
	defer h.wg.Done()
	ticker := time.NewTicker(refreshRate)
	defer ticker.Stop()

	key := exclusiveLockKey
	if h.lock.IsShared {
		key = fmt.Sprintf("%s%s", sharedLockPrefix, h.lock.AcquiredAt)
	}

	consecutiveFailures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := readLockByKey(ctx, h.store, key)
			if err != nil {
				consecutiveFailures++
				if consecutiveFailures >= maxRefreshFailures {
					return
				}
				continue
			}
			if !h.lock.ownsLock(current) {
				return
			}
			h.lock.ExpiresAt = time.Now().Add(lockTTL).Format(time.RFC3339Nano)
			if err := writeLockByKey(ctx, h.store, key, h.lock); err != nil {
				consecutiveFailures++
				if consecutiveFailures >= maxRefreshFailures {
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
	existing, err := readLockByKey(ctx, s, exclusiveLockKey)
	if err == nil {
		expires, parseErr := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
		if parseErr != nil || time.Now().Before(expires) {
			return fmt.Errorf(
				"repository is exclusively locked by %s (operation: %s) — try again later",
				existing.Holder, existing.Operation,
			)
		}
	} else {
		exists, existsErr := s.Exists(ctx, exclusiveLockKey)
		if existsErr != nil {
			return fmt.Errorf("check repo lock: %w", existsErr)
		}
		if exists {
			return fmt.Errorf("failed to read existing repo lock: %w", err)
		}
	}

	return checkSharedLocks(ctx, s)
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
			// Ignore read errors for individual shared locks, could be concurrent delete
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
