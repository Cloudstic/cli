package ui

import (
	"testing"

	"github.com/jedib0t/go-pretty/v6/progress"
)

func TestSafeLogWriter_WriteNoProgressWriter(t *testing.T) {
	w := &SafeLogWriter{}
	// Writing without a progress writer goes to stderr; should not panic.
	n, err := w.Write([]byte("hello world\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len("hello world\n") {
		t.Errorf("expected %d bytes written, got %d", len("hello world\n"), n)
	}
}

func TestSafeLogWriter_WriteEmptyMessage(t *testing.T) {
	w := &SafeLogWriter{}
	// A message that trims to empty should be a no-op.
	n, err := w.Write([]byte("\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 byte written, got %d", n)
	}
}

func TestSafeLogWriter_WriteEmptyBytes(t *testing.T) {
	w := &SafeLogWriter{}
	n, err := w.Write([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}
}

func TestSafeLogWriter_SetAndClearActive(t *testing.T) {
	w := &SafeLogWriter{}

	pw := progress.NewWriter()
	w.SetActive(pw)

	// After setting, pw field should be non-nil (exercise SetActive code path).
	w.mu.Lock()
	set := w.pw != nil
	w.mu.Unlock()
	if !set {
		t.Error("expected pw to be set after SetActive")
	}

	// Write with active progress writer - exercises the pw.Log path.
	// (Output goes to progress writer, not visible in test, but covers the branch.)
	_, _ = w.Write([]byte("test message\n"))

	w.ClearActive()
	w.mu.Lock()
	cleared := w.pw == nil
	w.mu.Unlock()
	if !cleared {
		t.Error("expected pw to be nil after ClearActive")
	}
}
