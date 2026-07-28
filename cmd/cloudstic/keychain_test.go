package main

import (
	"context"
	"os"
	"testing"

	"github.com/moby/term"
)

// Chain composition is tested in pkg/open, which owns it. What is left here is
// the one decision that stayed behind: whether an interactive prompt is
// possible at all.
//
// pkg/open deliberately refuses to guess that — a library's answer to "can I
// prompt?" is not a terminal program's — so it prompts only when given the
// functions to prompt with. Supplying them, or not, is this package's job.

// TestPasswordPrompts_RequiresATerminal is the successor to the
// characterization test written before pkg/open existed. That one recorded a
// branch that could not be reached under `go test` because it read
// os.Stdin directly; this one asserts the decision that replaced it.
func TestPasswordPrompts_RequiresATerminal(t *testing.T) {
	resolve, wrap := passwordPrompts()

	if term.IsTerminal(os.Stdin.Fd()) {
		if resolve == nil || wrap == nil {
			t.Error("on a terminal, both prompt functions must be supplied")
		}
		return
	}

	if resolve != nil || wrap != nil {
		t.Error("without a terminal there is nothing to prompt on, so no prompt " +
			"functions may be supplied — otherwise a non-interactive run blocks " +
			"on a read that will never be answered")
	}
}

// TestBuildKeychain_NoPromptCredentialWithoutATerminal joins the two halves:
// the decision made here, and the chain pkg/open builds from it. An empty
// config would take a prompt if one were available, so this is the case where
// the terminal check is the only thing deciding.
func TestBuildKeychain_NoPromptCredentialWithoutATerminal(t *testing.T) {
	if term.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is a terminal; this characterizes the non-interactive path")
	}

	chain, err := buildKeychain(context.Background(), unlockConfig{})
	if err != nil {
		t.Fatalf("buildKeychain: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("chain has %d credential(s), want none: without a terminal the "+
			"prompt credential must not be added, which leaves nothing to unlock with",
			len(chain))
	}
}

// TestBuildKeychain_StillCarriesConfiguredCredentials guards against the
// adapter dropping the configuration on the way through to pkg/open.
func TestBuildKeychain_StillCarriesConfiguredCredentials(t *testing.T) {
	chain, err := buildKeychain(context.Background(), unlockConfig{Password: "hunter2"})
	if err != nil {
		t.Fatalf("buildKeychain: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("chain has %d credential(s), want exactly the configured password", len(chain))
	}
}
