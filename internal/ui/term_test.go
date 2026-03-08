package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestTermWriter_Heading(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.Heading("My Title")
	got := buf.String()
	if !strings.Contains(got, "My Title") {
		t.Errorf("expected 'My Title' in output, got %q", got)
	}
}

func TestTermWriter_HeadingSub(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.HeadingSub("Title", "subtitle")
	got := buf.String()
	if !strings.Contains(got, "Title") || !strings.Contains(got, "subtitle") {
		t.Errorf("expected both title and subtitle in output, got %q", got)
	}
}

func TestTermWriter_Command_WithArgs(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.Command("backup", "--verbose")
	got := buf.String()
	if !strings.Contains(got, "backup") || !strings.Contains(got, "--verbose") {
		t.Errorf("expected command and args in output, got %q", got)
	}
}

func TestTermWriter_Command_NoArgs(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.Command("backup", "")
	got := buf.String()
	if !strings.Contains(got, "backup") {
		t.Errorf("expected command in output, got %q", got)
	}
}

func TestTermWriter_Commands(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.Commands([][2]string{{"init", "Initialize repo"}, {"backup", "Run backup"}})
	got := buf.String()
	if !strings.Contains(got, "init") || !strings.Contains(got, "backup") {
		t.Errorf("expected commands in output, got %q", got)
	}
}

func TestTermWriter_Flags(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.Flags([][2]string{{"--verbose", "Enable verbose"}, {"--dry-run", "Dry run"}})
	got := buf.String()
	if !strings.Contains(got, "--verbose") || !strings.Contains(got, "--dry-run") {
		t.Errorf("expected flags in output, got %q", got)
	}
}

func TestTermWriter_Note(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.Note("line one", "line two")
	got := buf.String()
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Errorf("expected notes in output, got %q", got)
	}
}

func TestTermWriter_Examples(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.Examples("cloudstic backup /home", "cloudstic restore latest")
	got := buf.String()
	if !strings.Contains(got, "cloudstic backup") {
		t.Errorf("expected examples in output, got %q", got)
	}
}

func TestTermWriter_Blank(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTermWriter(&buf)
	tw.Blank()
	if buf.String() != "\n" {
		t.Errorf("expected newline, got %q", buf.String())
	}
}

func TestEnv(t *testing.T) {
	result := Env("My description", "MY_VAR")
	if !strings.Contains(result, "My description") {
		t.Errorf("expected description in result, got %q", result)
	}
	if !strings.Contains(result, "MY_VAR") {
		t.Errorf("expected env var in result, got %q", result)
	}
}
