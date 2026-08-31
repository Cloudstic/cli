package onboarding

import (
	"strings"
	"testing"
)

// TestValidateRefName covers the naming rule the profiles file imposes: the
// names become YAML keys and appear in -profile/-store-ref arguments.
//
// It exercises ValidateRefName rather than the regexp behind it, which is what
// callers actually depend on — the CLI's version of this test reached into the
// package variable, so the rule could not move without the test breaking.
func TestValidateRefName(t *testing.T) {
	for _, name := range []string{"abc", "a-b", "a.b", "a_b", "A1", "test-store.v2"} {
		if err := ValidateRefName("store", name); err != nil {
			t.Errorf("expected %q to be valid, got %v", name, err)
		}
	}

	for _, name := range []string{"", "-abc", ".abc", "_abc", "a b", "a!b", "a@b"} {
		if err := ValidateRefName("store", name); err == nil {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

// TestValidateRefName_NamesTheKind checks the error says what was being named,
// since the same rule serves stores, auth entries and profiles.
func TestValidateRefName_NamesTheKind(t *testing.T) {
	err := ValidateRefName("auth", "bad name")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "invalid auth name") {
		t.Errorf("error should name the kind, got %q", got)
	}
}
