package e2e

import (
	"strings"
	"testing"
)

func TestCLI_Feature_TUI_Help(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}
	t.Parallel()

	bin := buildBinary(t)
	out := run(t, bin, "tui", "--help")
	// Help is rendered from the command declaration, so assert on the command
	// line and the flag tui declares rather than on a hand-written banner.
	for _, want := range []string{"cloudstic tui", "-profiles-file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output missing %q:\n%s", want, out)
		}
	}
}

func TestCLI_Feature_TUI_NonInteractiveGuardrail(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}
	t.Parallel()

	bin := buildBinary(t)
	out := runExpectFail(t, bin, "tui")
	if !strings.Contains(out, "requires an interactive terminal") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}
