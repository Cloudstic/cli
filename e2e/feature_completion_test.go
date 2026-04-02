package e2e

import (
	"strings"
	"testing"
)

// TestCLI_Feature_Completion verifies that shell completion scripts are
// generated for all supported shells and that an unsupported shell returns
// an error. This test does not require a store or source.
func TestCLI_Feature_Completion(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)

	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			out := run(t, bin, "completion", shell)
			if out == "" {
				t.Fatalf("completion %s produced empty output", shell)
			}
			if !strings.Contains(out, "cloudstic") {
				t.Errorf("completion %s output missing 'cloudstic'", shell)
			}
		})
	}

	out := runExpectFail(t, bin, "completion", "powershell")
	if !strings.Contains(out, "Unsupported shell") {
		t.Errorf("expected unsupported shell error, got: %s", out)
	}

	out = runExpectFail(t, bin, "completion")
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage message, got: %s", out)
	}
}
