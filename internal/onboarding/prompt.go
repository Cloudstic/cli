// Package onboarding holds the interactive flows that create a store, a
// profile, or an auth entry: the parts of `cloudstic store new`,
// `profile new`, `auth new` and `setup` that decide what to write, as opposed
// to the parts that parse a command line and print a result.
//
// It exists because those flows had grown into the presentation layer.
// runProfileNew was 186 lines interleaving prompting, validation, config
// mutation, live store probing and saving; runStoreNew, runAuthNew and
// runSetupWorkstation were the same shape at 105-115 lines. That is the
// arrangement `nomad setup vault` has — a ~200-line Run() with an os.Exit in
// the middle of it — and it is untestable for the same reason. The `gh` CLI
// puts the equivalent flow in pkg/cmd/auth/shared/login_flow.go behind a small
// Prompt interface, and its test file is larger than its implementation
// (issue #570).
//
// Nothing here touches a terminal. Everything a human is asked arrives through
// Prompter, which the CLI's runner satisfies and a test can stub.
package onboarding

import (
	"context"
	"fmt"
	"strings"
)

// Prompter is the terminal this package is driven through.
//
// It is deliberately the narrow set these flows actually use, rather than the
// CLI runner's whole surface: a caller supplying these six methods can run
// every flow in this package, and a test supplying them needs no terminal, no
// runner, and no output capture.
type Prompter interface {
	// CanPrompt reports whether a human can be asked. False in a script, under
	// -no-prompt, or when either end is not a terminal.
	CanPrompt() bool
	PromptLine(ctx context.Context, label, defaultValue string) (string, error)
	PromptValidatedLine(ctx context.Context, label, defaultValue string, validate func(string) error) (string, error)
	PromptConfirm(ctx context.Context, label string, defaultYes bool) (bool, error)
	PromptSelect(ctx context.Context, label string, options []string) (string, error)
	PromptSecret(ctx context.Context, label string) (string, error)
}

// Field describes one value a flow needs, and how to obtain it when the caller
// did not supply it.
type Field struct {
	// Label is the prompt shown to a human, e.g. "Profile name".
	Label string
	// Noun names the value inside the retry message shown after a rejected
	// answer ("<Noun> is required"). Empty means the lower-cased Label, which
	// is right except where the label contains an initialism — "Source URI"
	// must not become "source uri".
	Noun string
	// Missing is returned verbatim when nothing supplies a value and nobody can
	// be asked. It names the flag a script should have passed, so it is written
	// out rather than derived: "-store-ref is required (or provide -store to
	// create a new one)" is not a function of any label.
	Missing string
	// Default is offered as the pre-filled answer.
	Default string
	// Validate rejects an unusable value. It runs on a value the caller
	// supplied as well as on a prompted one, so a bad flag is reported rather
	// than stored.
	Validate func(string) error
}

func (f Field) noun() string {
	if f.Noun != "" {
		return f.Noun
	}
	return strings.ToLower(f.Label)
}

// Resolve returns current when the caller supplied one, otherwise asks for it,
// otherwise reports that it is missing.
//
// This is the shape that had been written out fifteen times across the four
// wizard commands — four times inside runProfileNew alone:
//
//	if a.source == "" {
//		if r.canPrompt() { ...twelve lines of prompt and validate... }
//		if a.source == "" { return r.fail("-source is required") }
//	}
//
// Writing it once is most of why those functions were long, and all of why the
// non-interactive path through them was easy to get subtly wrong: the inner
// re-check of the same variable is load-bearing, and there was nothing forcing
// each copy to have it.
func Resolve(ctx context.Context, p Prompter, current string, f Field) (string, error) {
	if current != "" {
		if f.Validate != nil {
			if err := f.Validate(current); err != nil {
				return "", err
			}
		}
		return current, nil
	}

	if p != nil && p.CanPrompt() {
		v, err := p.PromptValidatedLine(ctx, f.Label, f.Default, func(s string) error {
			if s == "" {
				return fmt.Errorf("%s is required", f.noun())
			}
			if f.Validate != nil {
				return f.Validate(s)
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("failed to read %s: %w", f.noun(), err)
		}
		if v != "" {
			return v, nil
		}
	}

	return "", fmt.Errorf("%s", f.Missing)
}
