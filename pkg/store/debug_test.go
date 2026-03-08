package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestDebugStore_Operations(t *testing.T) {
	ctx := context.Background()

	tmp, err := os.MkdirTemp("", "cloudstic-debug-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	inner, err := NewLocalStore(tmp)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ds := NewDebugStore(inner, &buf)

	if ds.Unwrap() != inner {
		t.Error("Unwrap should return the inner store")
	}

	// Put
	if err := ds.Put(ctx, "test/key", []byte("hello")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if !strings.Contains(buf.String(), "PUT") {
		t.Error("expected PUT in log output")
	}

	// Get
	buf.Reset()
	data, err := ds.Get(ctx, "test/key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("unexpected data: %q", data)
	}
	if !strings.Contains(buf.String(), "GET") {
		t.Error("expected GET in log output")
	}

	// Exists
	buf.Reset()
	exists, err := ds.Exists(ctx, "test/key")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("expected key to exist")
	}
	if !strings.Contains(buf.String(), "EXISTS") {
		t.Error("expected EXISTS in log output")
	}

	// Size
	buf.Reset()
	size, err := ds.Size(ctx, "test/key")
	if err != nil {
		t.Fatalf("Size failed: %v", err)
	}
	if size != 5 {
		t.Errorf("expected size 5, got %d", size)
	}
	if !strings.Contains(buf.String(), "SIZE") {
		t.Error("expected SIZE in log output")
	}

	// TotalSize
	buf.Reset()
	_, err = ds.TotalSize(ctx)
	if err != nil {
		t.Fatalf("TotalSize failed: %v", err)
	}
	if !strings.Contains(buf.String(), "TOTAL_SIZE") {
		t.Error("expected TOTAL_SIZE in log output")
	}

	// List
	buf.Reset()
	keys, err := ds.List(ctx, "test/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 key, got %d", len(keys))
	}
	if !strings.Contains(buf.String(), "LIST") {
		t.Error("expected LIST in log output")
	}

	// Delete
	buf.Reset()
	if err := ds.Delete(ctx, "test/key"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if !strings.Contains(buf.String(), "DELETE") {
		t.Error("expected DELETE in log output")
	}

	// Flush (inner doesn't implement Flush, should still log)
	buf.Reset()
	if err := ds.Flush(ctx); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if !strings.Contains(buf.String(), "FLUSH") {
		t.Error("expected FLUSH in log output")
	}
}

func TestDebugStore_LogsErrors(t *testing.T) {
	ctx := context.Background()

	tmp, err := os.MkdirTemp("", "cloudstic-debug-err-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	inner, err := NewLocalStore(tmp)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ds := NewDebugStore(inner, &buf)

	// Get on missing key should log the error.
	_, getErr := ds.Get(ctx, "nonexistent/key")
	if getErr == nil {
		t.Fatal("expected error for missing key")
	}
	if !strings.Contains(buf.String(), "err=") {
		t.Errorf("expected 'err=' in log output for failed Get, got: %q", buf.String())
	}
}

func TestDebugStore_Flush_WithFlusher(t *testing.T) {
	ctx := context.Background()

	var buf bytes.Buffer
	inner := &flushableStore{err: nil}
	ds := NewDebugStore(inner, &buf)

	if err := ds.Flush(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inner.flushed {
		t.Error("expected inner Flush to be called")
	}
}

func TestDebugStore_Flush_WithFlusherError(t *testing.T) {
	ctx := context.Background()

	var buf bytes.Buffer
	flushErr := errors.New("flush failed")
	inner := &flushableStore{err: flushErr}
	ds := NewDebugStore(inner, &buf)

	if err := ds.Flush(ctx); !errors.Is(err, flushErr) {
		t.Fatalf("expected flush error, got: %v", err)
	}
}

func TestFmtBytes(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{500, "500B"},
		{1024, "1.0KB"},
		{2048, "2.0KB"},
		{1 << 20, "1.0MB"},
		{2 << 20, "2.0MB"},
	}
	for _, tc := range tests {
		got := fmtBytes(tc.input)
		if got != tc.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// flushableStore is a minimal ObjectStore that also implements Flush.
type flushableStore struct {
	flushed bool
	err     error
}

func (f *flushableStore) Put(_ context.Context, _ string, _ []byte) error  { return nil }
func (f *flushableStore) Get(_ context.Context, _ string) ([]byte, error)  { return nil, nil }
func (f *flushableStore) Exists(_ context.Context, _ string) (bool, error) { return false, nil }
func (f *flushableStore) Delete(_ context.Context, _ string) error         { return nil }
func (f *flushableStore) List(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (f *flushableStore) Size(_ context.Context, _ string) (int64, error) { return 0, nil }
func (f *flushableStore) TotalSize(_ context.Context) (int64, error)      { return 0, nil }
func (f *flushableStore) Flush(_ context.Context) error {
	f.flushed = true
	return f.err
}
