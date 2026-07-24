package main

import (
	"context"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudstic/cli/internal/tui"
)

// tuiRunBubbleTea is overridable in tests so the launcher can be exercised
// without a real terminal program.
var tuiRunBubbleTea = func(ctx context.Context, model tea.Model, opts ...tea.ProgramOption) error {
	_, err := tea.NewProgram(model, append(opts, tea.WithContext(ctx))...).Run()
	return err
}

// runBubbleTeaTUI launches the experimental Bubble Tea renderer with an
// already-built dashboard. It is the parallel, read-only renderer added in
// RFC 0012 Phase 2 (issue #338); the legacy renderer remains the default.
func runBubbleTeaTUI(r *runner, ctx context.Context, dashboard tui.Dashboard) int {
	stdin := r.stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	model := tui.NewModel(dashboard)
	opts := []tea.ProgramOption{
		tea.WithInput(stdin),
		tea.WithOutput(r.out),
		tea.WithAltScreen(),
	}
	if err := tuiRunBubbleTea(ctx, model, opts...); err != nil {
		return r.fail("TUI exited with error: %v", err)
	}
	return 0
}
