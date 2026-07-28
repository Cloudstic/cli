package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestDebugf_WriterNil(t *testing.T) {
	Writer = nil
	// Should not panic.
	Debugf("hello %s", "world")
}

func TestDebugf_WritesMessage(t *testing.T) {
	var buf bytes.Buffer
	Writer = &buf
	t.Cleanup(func() { Writer = nil })

	Debugf("hello %s", "world")

	got := buf.String()
	if !strings.Contains(got, "hello world") {
		t.Errorf("expected 'hello world' in output, got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected newline at end, got %q", got)
	}
}

func TestIsDebug(t *testing.T) {
	Writer = nil
	if IsDebug() {
		t.Error("expected IsDebug() == false when Writer is nil")
	}

	var buf bytes.Buffer
	Writer = &buf
	t.Cleanup(func() { Writer = nil })

	if !IsDebug() {
		t.Error("expected IsDebug() == true when Writer is set")
	}
}

func TestNew_WithColor(t *testing.T) {
	var buf bytes.Buffer
	Writer = &buf
	t.Cleanup(func() { Writer = nil })

	l := New("mycomp", ColorCyan)
	l.Debugf("msg %d", 42)

	got := buf.String()
	if !strings.Contains(got, "mycomp") {
		t.Errorf("expected component name in output, got %q", got)
	}
	if !strings.Contains(got, "msg 42") {
		t.Errorf("expected message in output, got %q", got)
	}
}

func TestNew_NoColor(t *testing.T) {
	var buf bytes.Buffer
	Writer = &buf
	t.Cleanup(func() { Writer = nil })

	l := New("comp", "")
	l.Debugf("test")

	got := buf.String()
	if !strings.Contains(got, "[comp]") {
		t.Errorf("expected '[comp]' in output, got %q", got)
	}
}

func TestNew_NoComponent(t *testing.T) {
	var buf bytes.Buffer
	Writer = &buf
	t.Cleanup(func() { Writer = nil })

	l := New("", "")
	l.Debugf("bare message")

	got := buf.String()
	if got != "bare message\n" {
		t.Errorf("expected 'bare message\\n', got %q", got)
	}
}

func TestLogger_Debugf_WriterNil(t *testing.T) {
	Writer = nil
	l := New("comp", ColorRed)
	// Should not panic.
	l.Debugf("hello")
}

// TestLoggerTo_BoundSinkBeatsTheGlobal is the property the injectable sink
// exists for: two loggers in one process can write to different places, which
// the package-level Writer alone cannot express.
func TestLoggerTo_BoundSinkBeatsTheGlobal(t *testing.T) {
	Writer = nil
	defer func() { Writer = nil }()

	var global, boundA, boundB bytes.Buffer
	Writer = &global

	base := New("comp", "")
	base.To(&boundA).Debugf("to a")
	base.To(&boundB).Debugf("to b")
	base.Debugf("to the fallback")

	if got := boundA.String(); got != "[comp] to a\n" {
		t.Errorf("bound sink A = %q", got)
	}
	if got := boundB.String(); got != "[comp] to b\n" {
		t.Errorf("bound sink B = %q", got)
	}
	if got := global.String(); got != "[comp] to the fallback\n" {
		t.Errorf("fallback = %q, want only the unbound logger's line", got)
	}
}

// TestLoggerTo_NilSinkFallsBack lets a caller pass a sink through
// unconditionally instead of branching on whether it has one.
func TestLoggerTo_NilSinkFallsBack(t *testing.T) {
	Writer = nil
	defer func() { Writer = nil }()

	var global bytes.Buffer
	Writer = &global

	New("comp", "").To(nil).Debugf("still logged")

	if got := global.String(); got != "[comp] still logged\n" {
		t.Errorf("got %q, want the message on the fallback", got)
	}
}

// TestLoggerTo_KeepsPrefixAndDoesNotMutate guards the copy: binding a sink must
// not change the logger it was derived from.
func TestLoggerTo_KeepsPrefixAndDoesNotMutate(t *testing.T) {
	Writer = nil
	defer func() { Writer = nil }()

	var bound, global bytes.Buffer
	Writer = &global

	base := New("comp", ColorCyan)
	derived := base.To(&bound)

	derived.Debugf("x")
	base.Debugf("y")

	if !strings.Contains(bound.String(), "[comp]") {
		t.Errorf("derived logger lost its prefix: %q", bound.String())
	}
	if !strings.Contains(global.String(), "y") {
		t.Error("binding a sink must not redirect the logger it was derived from")
	}
}
