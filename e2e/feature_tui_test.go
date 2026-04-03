package e2e

import (
	"strings"
	"testing"
)

func TestCLI_Feature_TUI_Help(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)
	out := run(t, bin, "tui", "--help")
	if !strings.Contains(out, "Usage: cloudstic tui [options]") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestCLI_Feature_TUI_NonInteractiveGuardrail(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)
	out := runExpectFail(t, bin, "tui")
	if !strings.Contains(out, "requires an interactive terminal") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}
