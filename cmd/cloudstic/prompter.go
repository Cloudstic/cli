package main

import (
	"context"

	"github.com/cloudstic/cli/internal/onboarding"
)

// onboardingPrompter adapts the runner to onboarding.Prompter.
//
// An adapter rather than exported methods on runner: the runner's prompt
// methods are unexported I/O primitives and should stay that way
// (TestRunnerMethodsAreIOPrimitivesOnly), and the interface is what keeps
// internal/onboarding from depending on this package at all.
type onboardingPrompter struct{ r *runner }

func (p onboardingPrompter) CanPrompt() bool { return p.r.canPrompt() }

func (p onboardingPrompter) PromptLine(ctx context.Context, label, def string) (string, error) {
	return p.r.promptLine(ctx, label, def)
}

func (p onboardingPrompter) PromptValidatedLine(ctx context.Context, label, def string, validate func(string) error) (string, error) {
	return p.r.promptValidatedLine(ctx, label, def, validate)
}

func (p onboardingPrompter) PromptConfirm(ctx context.Context, label string, defaultYes bool) (bool, error) {
	return p.r.promptConfirm(ctx, label, defaultYes)
}

func (p onboardingPrompter) PromptSelect(ctx context.Context, label string, options []string) (string, error) {
	return p.r.promptSelect(ctx, label, options)
}

func (p onboardingPrompter) PromptSecret(ctx context.Context, label string) (string, error) {
	return p.r.promptSecret(ctx, label)
}

// prompterFor returns the runner as the terminal internal/onboarding drives.
//
// A free function, not a method: TestRunnerMethodsAreIOPrimitivesOnly keeps the
// runner to the primitives it owns, and building an adapter is not one.
func prompterFor(r *runner) onboarding.Prompter { return onboardingPrompter{r} }

var _ onboarding.Prompter = onboardingPrompter{}
