package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogger_WritesToItsSink(t *testing.T) {
	var buf bytes.Buffer
	New("comp", "").To(&buf).Debugf("hello %s", "world")

	if got, want := buf.String(), "[comp] hello world\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestLogger_DiscardsWithoutASink is the property that replaced the
// package-level writer: a logger nobody gave a sink writes nowhere, rather
// than falling back to a process-wide destination. There is no longer a global
// for it to reach, so the assertion is that neither call panics or is required
// to be guarded by the caller.
func TestLogger_DiscardsWithoutASink(t *testing.T) {
	New("comp", "").Debugf("dropped")
	New("comp", "").To(nil).Debugf("also dropped")
}

// TestLogger_NilReceiverIsSafe lets a component hold a logger it was never
// given without guarding every call site.
func TestLogger_NilReceiverIsSafe(t *testing.T) {
	var l *Logger
	l.Debugf("must not panic")

	if got := l.To(nil); got != nil {
		t.Error("To on a nil logger must stay nil")
	}
}

// TestLogger_SinksAreIndependent is the property the global could not express:
// two loggers in one process writing to different places at once.
func TestLogger_SinksAreIndependent(t *testing.T) {
	var a, b bytes.Buffer
	base := New("comp", "")

	base.To(&a).Debugf("to a")
	base.To(&b).Debugf("to b")

	if got := a.String(); got != "[comp] to a\n" {
		t.Errorf("sink A = %q", got)
	}
	if got := b.String(); got != "[comp] to b\n" {
		t.Errorf("sink B = %q", got)
	}
}

// TestLogger_ToDoesNotMutateItsSource guards the copy: binding a sink must not
// redirect the logger it was derived from.
func TestLogger_ToDoesNotMutateItsSource(t *testing.T) {
	var bound, later bytes.Buffer

	base := New("comp", ColorCyan)
	base.To(&bound).Debugf("x")
	base.To(&later).Debugf("y")

	if !strings.Contains(bound.String(), "[comp]") {
		t.Errorf("derived logger lost its prefix: %q", bound.String())
	}
	if strings.Contains(bound.String(), "y") {
		t.Error("the second binding leaked into the first logger's sink")
	}
	if !strings.Contains(later.String(), "y") {
		t.Errorf("second sink = %q, want the second message", later.String())
	}
}

func TestLogger_ColorAndPrefix(t *testing.T) {
	var buf bytes.Buffer
	New("comp", ColorCyan).To(&buf).Debugf("msg")

	out := buf.String()
	if !strings.Contains(out, ColorCyan) || !strings.Contains(out, ColorReset) {
		t.Errorf("expected the colour codes to survive, got %q", out)
	}
	if !strings.HasSuffix(out, "msg\n") {
		t.Errorf("expected the message last, got %q", out)
	}
}

func TestLogger_EmptyComponentHasNoPrefix(t *testing.T) {
	var buf bytes.Buffer
	New("", "").To(&buf).Debugf("bare")

	if got, want := buf.String(), "bare\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
