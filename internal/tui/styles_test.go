package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// forceColor renders with a fixed color profile so the assertions below do not
// depend on whether the test binary's stdout happens to be a terminal.
func forceColor(t *testing.T, style lipgloss.Style, text string) string {
	t.Helper()
	return style.Renderer(lipgloss.NewRenderer(&strings.Builder{})).Render(text)
}

// The legacy renderer painted "warning" cyan and left "error" unstyled, which
// inverted the hierarchy: the most severe state was the least visible. Each
// state must now be distinctly styled, and error must never be the plain one.
func TestThemeStateStyles_AreDistinctAndSeverityOrdered(t *testing.T) {
	th := newThemeWithColor(true)

	statuses := map[ProfileStatus]lipgloss.Style{
		ProfileStatusReady:    th.stateReady,
		ProfileStatusWarning:  th.stateWarning,
		ProfileStatusError:    th.stateError,
		ProfileStatusDisabled: th.stateDisabled,
	}
	seen := map[lipgloss.TerminalColor]ProfileStatus{}
	for status, want := range statuses {
		got := th.stateStyle(status)
		if got.GetForeground() != want.GetForeground() {
			t.Fatalf("status %q mapped to the wrong style", status)
		}
		fg := got.GetForeground()
		if fg == (lipgloss.NoColor{}) {
			t.Fatalf("status %q renders with no color", status)
		}
		if prev, dup := seen[fg]; dup {
			t.Fatalf("statuses %q and %q share color %v", prev, status, fg)
		}
		seen[fg] = status
	}

	if !th.stateError.GetBold() {
		t.Fatalf("error state should be the most prominent, but is not bold")
	}
	if th.stateWarning.GetForeground() == th.accent.GetForeground() {
		t.Fatalf("warning must not reuse the accent color it was confused with")
	}
}

func TestThemeWithoutColor_DropsForegroundsButKeepsEmphasis(t *testing.T) {
	th := newThemeWithColor(false)

	for name, style := range map[string]lipgloss.Style{
		"title":         th.title,
		"accent":        th.accent,
		"subtle":        th.subtle,
		"stateReady":    th.stateReady,
		"stateWarning":  th.stateWarning,
		"stateError":    th.stateError,
		"stateDisabled": th.stateDisabled,
	} {
		if style.GetForeground() != (lipgloss.NoColor{}) {
			t.Fatalf("style %q still carries a foreground color under NO_COLOR", name)
		}
	}
	if !th.stateError.GetBold() || !th.title.GetBold() {
		t.Fatalf("NO_COLOR should drop color only, not bold emphasis")
	}
	if !th.panel.GetBorderTop() {
		t.Fatalf("NO_COLOR should keep panel borders")
	}
}

func TestNewTheme_HonorsNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if colorEnabled() {
		t.Fatalf("NO_COLOR=1 should disable color")
	}
	if got := forceColor(t, newTheme().stateError, "error"); strings.Contains(got, "\x1b[") {
		t.Fatalf("NO_COLOR rendered escape sequences: %q", got)
	}

	t.Setenv("NO_COLOR", "")
	if !colorEnabled() {
		t.Fatalf("empty NO_COLOR should leave color enabled")
	}
}
