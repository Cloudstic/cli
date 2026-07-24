package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudstic/cli/internal/tui"
)

func TestRunBubbleTeaTUI_Success(t *testing.T) {
	restore := swapBubbleTeaRunner(func(context.Context, tea.Model, ...tea.ProgramOption) error {
		return nil
	})
	defer restore()

	r := &runner{out: &strings.Builder{}, errOut: &strings.Builder{}}
	if code := runBubbleTeaTUI(r, context.Background(), tui.Dashboard{}); code != 0 {
		t.Fatalf("exit code=%d want 0", code)
	}
}

func TestRunBubbleTeaTUI_Error(t *testing.T) {
	restore := swapBubbleTeaRunner(func(context.Context, tea.Model, ...tea.ProgramOption) error {
		return errors.New("boom")
	})
	defer restore()

	errOut := &strings.Builder{}
	r := &runner{out: &strings.Builder{}, errOut: errOut}
	if code := runBubbleTeaTUI(r, context.Background(), tui.Dashboard{}); code == 0 {
		t.Fatalf("exit code=0 want non-zero on error")
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("errOut missing error detail: %q", errOut.String())
	}
}

func swapBubbleTeaRunner(fn func(context.Context, tea.Model, ...tea.ProgramOption) error) func() {
	prev := tuiRunBubbleTea
	tuiRunBubbleTea = fn
	return func() { tuiRunBubbleTea = prev }
}
