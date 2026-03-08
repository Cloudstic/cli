package ui

import (
	"testing"
)

func TestNoOpReporter(t *testing.T) {
	r := NewNoOpReporter()
	phase := r.StartPhase("test", 100, false)
	if phase == nil {
		t.Fatal("expected non-nil phase")
	}

	// All phase operations should be no-ops without panicking.
	phase.Increment(10)
	phase.Log("hello")
	phase.Done()
	phase.Error()
}

func TestNoOpReporter_ByteUnits(t *testing.T) {
	r := NewNoOpReporter()
	phase := r.StartPhase("upload", 1024, true)
	if phase == nil {
		t.Fatal("expected non-nil phase")
	}
	phase.Increment(512)
	phase.Done()
}

func TestConsoleReporter_SetLogWriter(t *testing.T) {
	r := NewConsoleReporter()
	w := &SafeLogWriter{}
	// Should not panic.
	r.SetLogWriter(w)
}
