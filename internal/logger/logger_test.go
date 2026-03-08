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
