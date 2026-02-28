package store

import (
	"context"
	"fmt"
	"testing"
)

func TestQuotaStore_UnderBudget(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	qs := NewQuotaStore(newMemStore(), 1000, cancel)

	if err := qs.Put(ctx, "chunk/aaa", make([]byte, 500)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("context should not be cancelled under budget")
	}
}

func TestQuotaStore_ExceedsBudget(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	qs := NewQuotaStore(newMemStore(), 100, cancel)

	if err := qs.Put(ctx, "chunk/aaa", make([]byte, 50)); err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("context should not be cancelled yet")
	}

	if err := qs.Put(ctx, "chunk/bbb", make([]byte, 60)); err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("context should be cancelled after exceeding budget")
	}
	if cause := context.Cause(ctx); cause != ErrQuotaExceeded {
		t.Fatalf("expected ErrQuotaExceeded, got %v", cause)
	}
}

func TestQuotaStore_ExactBudget(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	qs := NewQuotaStore(newMemStore(), 100, cancel)

	if err := qs.Put(ctx, "chunk/aaa", make([]byte, 100)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("context should not be cancelled at exactly the budget")
	}
}

func TestQuotaStore_MultiplePutsAccumulate(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	qs := NewQuotaStore(newMemStore(), 100, cancel)

	for i := range 10 {
		if err := qs.Put(ctx, fmt.Sprintf("chunk/%d", i), make([]byte, 10)); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("10x10 bytes = exactly budget, should not cancel")
	}

	if err := qs.Put(ctx, "chunk/overflow", make([]byte, 1)); err != nil {
		t.Fatalf("overflow Put: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("should be cancelled after exceeding 100 bytes")
	}
}

func TestQuotaStore_PutErrorDoesNotCount(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	failing := &failingStore{err: fmt.Errorf("disk full")}
	qs := NewQuotaStore(failing, 10, cancel)

	if err := qs.Put(ctx, "chunk/aaa", make([]byte, 50)); err == nil {
		t.Fatal("expected error from failing store")
	}
	if ctx.Err() != nil {
		t.Fatal("failed Put should not count toward quota")
	}
}

type failingStore struct {
	memStore
	err error
}

func (f *failingStore) Put(_ context.Context, _ string, _ []byte) error { return f.err }
