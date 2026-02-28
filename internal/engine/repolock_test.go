package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

func TestAcquireRepoLock_Success(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lock, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer lock.Release()

	data, err := s.Get(ctx, exclusiveLockKey)
	if err != nil {
		t.Fatalf("lock key should exist: %v", err)
	}
	var rl RepoLock
	if err := json.Unmarshal(data, &rl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rl.Operation != "prune" {
		t.Errorf("expected operation prune, got %s", rl.Operation)
	}
	if rl.Holder == "" {
		t.Error("holder should not be empty")
	}
}

func TestAcquireRepoLock_Conflict(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lock1, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer lock1.Release()

	_, err = AcquireRepoLock(ctx, s, "prune")
	if err == nil {
		t.Fatal("second acquire should fail while first lock is held")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error should mention locked, got: %v", err)
	}
}

func TestAcquireRepoLock_ExpiredLockIsOverridden(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	expired := RepoLock{
		Operation:  "prune",
		Holder:     "old-host (pid 1)",
		AcquiredAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		ExpiresAt:  time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
	data, _ := json.Marshal(expired)
	_ = s.Put(ctx, exclusiveLockKey, data)

	lock, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("should override expired lock: %v", err)
	}
	lock.Release()
}

func TestAcquireRepoLock_MalformedExpiresAtTreatedAsLocked(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	bad := RepoLock{
		Operation:  "prune",
		Holder:     "other-host (pid 1)",
		AcquiredAt: time.Now().Format(time.RFC3339Nano),
		ExpiresAt:  "not-a-timestamp",
	}
	data, _ := json.Marshal(bad)
	_ = s.Put(ctx, exclusiveLockKey, data)

	_, err := AcquireRepoLock(ctx, s, "backup")
	if err == nil {
		t.Fatal("malformed ExpiresAt should be treated as locked")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error should mention locked, got: %v", err)
	}
}

func TestRelease_DeletesOwnLock(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lock, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	lock.Release()

	exists, _ := s.Exists(ctx, exclusiveLockKey)
	if exists {
		t.Error("lock key should be deleted after release")
	}
}

func TestRelease_DoesNotDeleteOtherLock(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lock, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	lock.cancel()
	lock.wg.Wait()

	other := RepoLock{
		Operation:  "backup",
		Holder:     "other-host (pid 99)",
		AcquiredAt: time.Now().Format(time.RFC3339Nano),
		ExpiresAt:  time.Now().Add(lockTTL).Format(time.RFC3339Nano),
	}
	otherData, _ := json.Marshal(other)
	_ = s.Put(ctx, exclusiveLockKey, otherData)

	lock.Release()

	exists, _ := s.Exists(ctx, exclusiveLockKey)
	if !exists {
		t.Error("release should NOT delete a lock owned by another process")
	}
}

func TestCheckRepoLock_NoLock(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	if err := CheckRepoLock(ctx, s); err != nil {
		t.Errorf("should return nil when no lock: %v", err)
	}
}

func TestCheckRepoLock_ActiveLock(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lock, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Release()

	err = CheckRepoLock(ctx, s)
	if err == nil {
		t.Fatal("should return error when lock is active")
	}
	if !strings.Contains(err.Error(), "prune") {
		t.Errorf("error should mention operation, got: %v", err)
	}
}

func TestCheckRepoLock_ExpiredLock(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	expired := RepoLock{
		Operation:  "prune",
		Holder:     "old-host (pid 1)",
		AcquiredAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		ExpiresAt:  time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
	data, _ := json.Marshal(expired)
	_ = s.Put(ctx, exclusiveLockKey, data)

	if err := CheckRepoLock(ctx, s); err != nil {
		t.Errorf("expired lock should not block: %v", err)
	}
}

func TestCheckRepoLock_MalformedExpiresAtTreatedAsLocked(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	bad := RepoLock{
		Operation:  "prune",
		Holder:     "host (pid 1)",
		AcquiredAt: time.Now().Format(time.RFC3339Nano),
		ExpiresAt:  "garbage",
	}
	data, _ := json.Marshal(bad)
	_ = s.Put(ctx, exclusiveLockKey, data)

	err := CheckRepoLock(ctx, s)
	if err == nil {
		t.Fatal("malformed ExpiresAt should be treated as locked")
	}
}

func TestCheckRepoLock_CorruptedJSON(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	_ = s.Put(ctx, exclusiveLockKey, []byte("{invalid json"))

	err := CheckRepoLock(ctx, s)
	if err == nil {
		t.Fatal("corrupted lock JSON should return error")
	}
}

func TestLockRefresh(t *testing.T) {
	origTTL, origRate := lockTTL, refreshRate
	lockTTL = 2 * time.Second
	refreshRate = 200 * time.Millisecond
	defer func() { lockTTL, refreshRate = origTTL, origRate }()

	ctx := context.Background()
	s := NewMockStore()

	lock, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	data1, _ := s.Get(ctx, exclusiveLockKey)
	var rl1 RepoLock
	_ = json.Unmarshal(data1, &rl1)
	expires1, _ := time.Parse(time.RFC3339Nano, rl1.ExpiresAt)

	deadline := time.Now().Add(3 * time.Second)
	for {
		data2, _ := s.Get(ctx, exclusiveLockKey)
		var rl2 RepoLock
		_ = json.Unmarshal(data2, &rl2)
		expires2, _ := time.Parse(time.RFC3339Nano, rl2.ExpiresAt)

		if expires2.After(expires1) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock should be refreshed within deadline: expires1=%s", expires1)
		}
		time.Sleep(50 * time.Millisecond)
	}

	lock.Release()
}

func TestRefresh_StopsWhenOwnershipLost(t *testing.T) {
	origTTL, origRate := lockTTL, refreshRate
	lockTTL = 2 * time.Second
	refreshRate = 200 * time.Millisecond
	defer func() { lockTTL, refreshRate = origTTL, origRate }()

	ctx := context.Background()
	s := NewMockStore()

	lock, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Wait for at least one refresh cycle to complete so there is no
	// in-flight read-then-write that could straddle our overwrite.
	time.Sleep(refreshRate + 100*time.Millisecond)

	other := RepoLock{
		Operation:  "backup",
		Holder:     "other-host (pid 99)",
		AcquiredAt: time.Now().Format(time.RFC3339Nano),
		ExpiresAt:  time.Now().Add(lockTTL).Format(time.RFC3339Nano),
	}
	otherData, _ := json.Marshal(other)
	_ = s.Put(ctx, exclusiveLockKey, otherData)

	// Poll until the lock stays as the other holder across two consecutive
	// reads separated by a refresh interval (proves the goroutine stopped).
	deadline := time.Now().Add(3 * time.Second)
	for {
		time.Sleep(refreshRate + 100*time.Millisecond)

		data, _ := s.Get(ctx, exclusiveLockKey)
		var current RepoLock
		_ = json.Unmarshal(data, &current)

		if current.Holder == other.Holder {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh goroutine did not stop; lock holder is %q, want %q",
				current.Holder, other.Holder)
		}
	}

	lock.cancel()
	lock.wg.Wait()
}

func TestPruneDryRun_NoLock(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	metered := store.NewMeteredStore(s)
	pm := NewPruneManager(metered, ui.NewNoOpReporter())
	_, err := pm.Run(ctx, WithPruneDryRun())
	if err != nil {
		t.Fatalf("dry run should succeed: %v", err)
	}

	exists, _ := s.Exists(ctx, exclusiveLockKey)
	if exists {
		t.Error("dry run should not create a lock")
	}
}

func TestBackupDryRun_NoLock(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()
	src := NewMockSource()

	bm := NewBackupManager(src, s, ui.NewNoOpReporter(), WithBackupDryRun())
	_, err := bm.Run(ctx)
	if err != nil {
		t.Fatalf("dry run should succeed: %v", err)
	}

	exists, _ := s.Exists(ctx, exclusiveLockKey)
	if exists {
		t.Error("backup dry run should not create a lock")
	}
}

func TestAcquireSharedLock_Success(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lock, err := AcquireSharedLock(ctx, s, "backup")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer lock.Release()

	// Verify shared lock exists
	keys, err := s.List(ctx, sharedLockPrefix)
	if err != nil || len(keys) != 1 {
		t.Fatalf("expected 1 shared lock, got %d", len(keys))
	}

	data, err := s.Get(ctx, keys[0])
	if err != nil {
		t.Fatalf("lock key should exist: %v", err)
	}
	var rl RepoLock
	if err := json.Unmarshal(data, &rl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rl.Operation != "backup" {
		t.Errorf("expected operation backup, got %s", rl.Operation)
	}
	if !rl.IsShared {
		t.Error("Shared lock should have IsShared set to true")
	}
}

func TestAcquireSharedLock_ConcurrentSharedLocks(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lock1, err := AcquireSharedLock(ctx, s, "backup1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer lock1.Release()

	// Wait briefly to ensure AcquiredAt timestamp differs
	time.Sleep(1 * time.Millisecond)

	lock2, err := AcquireSharedLock(ctx, s, "backup2")
	if err != nil {
		t.Fatalf("second shared lock should succeed: %v", err)
	}
	defer lock2.Release()

	keys, _ := s.List(ctx, sharedLockPrefix)
	if len(keys) != 2 {
		t.Fatalf("expected 2 shared locks, got %d", len(keys))
	}
}

func TestAcquireSharedLock_ConflictWithExclusive(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lockExcl, err := AcquireRepoLock(ctx, s, "prune")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer lockExcl.Release()

	_, err = AcquireSharedLock(ctx, s, "backup")
	if err == nil {
		t.Fatal("shared lock should fail if exclusive lock exists")
	}
	if !strings.Contains(err.Error(), "exclusively locked") {
		t.Errorf("error should mention exclusively locked, got: %v", err)
	}
}

func TestAcquireExclusiveLock_ConflictWithShared(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	lockShared, err := AcquireSharedLock(ctx, s, "backup")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer lockShared.Release()

	_, err = AcquireRepoLock(ctx, s, "prune")
	if err == nil {
		t.Fatal("exclusive lock should fail if shared lock exists")
	}
	if !strings.Contains(err.Error(), "cannot acquire exclusive lock") {
		t.Errorf("expected error about existing shared lock, got: %v", err)
	}
}
