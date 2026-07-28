package main

import (
	"context"
	"os"

	"github.com/moby/term"

	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/open"
)

// The credential chain is assembled by pkg/open. What stays here is whether an
// interactive prompt is even possible, which pkg/open deliberately refuses to
// guess: it is this process's stdin that decides, and only a terminal program
// can answer that for itself (RFC 0022 §7).

// buildKeychain assembles the credential chain that unlocks a repository.
func buildKeychain(ctx context.Context, cfg unlockConfig) (keychain.Chain, error) {
	return open.Keychain(ctx, cfg, open.WithPasswordPrompt(passwordPrompts()))
}

// passwordPrompts returns the interactive prompt functions, or nil when this
// process has no terminal to prompt on.
//
// Returning nil is how "do not prompt" is expressed to pkg/open: without them
// no prompt credential is appended, so a non-interactive run fails with a
// missing-credential error instead of blocking on a read that will never be
// answered.
func passwordPrompts() (resolve, wrap func() (string, error)) {
	if !term.IsTerminal(os.Stdin.Fd()) {
		return nil, nil
	}
	return func() (string, error) { return ui.PromptPassword("Repository password") },
		func() (string, error) { return ui.PromptPasswordConfirm("Enter new repository password") }
}
