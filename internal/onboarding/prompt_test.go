package onboarding

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubPrompter is the whole test fixture this package needs. That is the point
// of the Prompter seam: before it, exercising these flows meant constructing a
// cmd/cloudstic runner with output buffers and a fake stdin.
type stubPrompter struct {
	canPrompt bool
	answers   []string
	asked     []string
	err       error
}

func (s *stubPrompter) CanPrompt() bool { return s.canPrompt }

func (s *stubPrompter) PromptValidatedLine(_ context.Context, label, def string, validate func(string) error) (string, error) {
	s.asked = append(s.asked, label)
	if s.err != nil {
		return "", s.err
	}
	for _, a := range s.answers {
		if validate != nil && validate(a) != nil {
			continue // a human would be re-asked; skip to the next answer
		}
		return a, nil
	}
	return def, nil
}

func (s *stubPrompter) PromptLine(context.Context, string, string) (string, error) { return "", nil }
func (s *stubPrompter) PromptConfirm(context.Context, string, bool) (bool, error)  { return false, nil }
func (s *stubPrompter) PromptSecret(context.Context, string) (string, error)       { return "", nil }
func (s *stubPrompter) PromptSelect(context.Context, string, []string) (string, error) {
	return "", nil
}

func TestResolve_SuppliedValueIsNotPromptedFor(t *testing.T) {
	p := &stubPrompter{canPrompt: true}
	got, err := Resolve(context.Background(), p, "given", Field{Label: "Profile name", Missing: "-name is required"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "given" {
		t.Errorf("got %q, want the supplied value", got)
	}
	if len(p.asked) != 0 {
		t.Errorf("a supplied value must not be prompted for, asked: %v", p.asked)
	}
}

// TestResolve_SuppliedValueIsStillValidated is why validation lives in Resolve
// rather than only in the prompt callback: a bad flag must be reported, not
// stored.
func TestResolve_SuppliedValueIsStillValidated(t *testing.T) {
	sentinel := errors.New("bad name")
	_, err := Resolve(context.Background(), &stubPrompter{canPrompt: true}, "bad", Field{
		Label:    "Profile name",
		Missing:  "-name is required",
		Validate: func(string) error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("a supplied value must be validated, got %v", err)
	}
}

func TestResolve_PromptsWhenEmpty(t *testing.T) {
	p := &stubPrompter{canPrompt: true, answers: []string{"typed"}}
	got, err := Resolve(context.Background(), p, "", Field{Label: "Profile name", Missing: "-name is required"})
	if err != nil || got != "typed" {
		t.Fatalf("got %q, %v; want the prompted answer", got, err)
	}
	if len(p.asked) != 1 || p.asked[0] != "Profile name" {
		t.Errorf("expected one prompt labelled %q, got %v", "Profile name", p.asked)
	}
}

// TestResolve_ReportsMissingWhenNobodyCanBeAsked is the non-interactive path —
// the one the fifteen hand-written copies of this shape each had to remember to
// re-check, and the reason the message is carried verbatim rather than derived:
// it names the flag a script should have passed.
func TestResolve_ReportsMissingWhenNobodyCanBeAsked(t *testing.T) {
	_, err := Resolve(context.Background(), &stubPrompter{canPrompt: false}, "", Field{
		Label:   "Store reference name",
		Missing: "-store-ref is required (or provide -store to create a new one)",
	})
	if err == nil || err.Error() != "-store-ref is required (or provide -store to create a new one)" {
		t.Fatalf("got %v, want the verbatim Missing message", err)
	}
}

func TestResolve_NilPrompterIsNonInteractive(t *testing.T) {
	if _, err := Resolve(context.Background(), nil, "", Field{Missing: "-name is required"}); err == nil {
		t.Fatal("a nil prompter must be treated as unable to ask")
	}
}

// TestResolve_NounOverridesTheLoweredLabel exists because lower-casing a label
// mangles an initialism: "Source URI" must retry as "source URI is required".
func TestResolve_NounOverridesTheLoweredLabel(t *testing.T) {
	var seen error
	p := &stubPrompter{canPrompt: true}
	p.answers = nil // force the validator to run against ""
	_, _ = Resolve(context.Background(), &recordingPrompter{p, &seen}, "", Field{
		Label: "Source URI", Noun: "source URI", Missing: "-source is required",
	})
	if seen == nil || !strings.Contains(seen.Error(), "source URI is required") {
		t.Fatalf("retry message should use Noun, got %v", seen)
	}
}

// recordingPrompter captures what the validator says about an empty answer.
type recordingPrompter struct {
	*stubPrompter
	seen *error
}

func (r *recordingPrompter) PromptValidatedLine(_ context.Context, label, def string, validate func(string) error) (string, error) {
	r.asked = append(r.asked, label)
	if validate != nil {
		*r.seen = validate("")
	}
	return def, nil
}
