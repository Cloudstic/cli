package backends

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/pkg/secretref"
)

// TestDefault_CoversEveryBuiltInScheme pins the built-in scheme list. A scheme
// dropped from here stops resolving for every caller, and a profile that
// referenced it starts failing as "unsupported scheme" rather than anything
// that points at the cause.
func TestDefault_CoversEveryBuiltInScheme(t *testing.T) {
	got := Default()
	for _, scheme := range []string{"env", "file", "config-token", "keychain", "wincred", "secret-service"} {
		if _, ok := got[scheme]; !ok {
			t.Errorf("Default() is missing the %q backend", scheme)
		}
	}
	if len(got) != 6 {
		t.Errorf("Default() has %d backends, want 6 — update this test deliberately when adding one", len(got))
	}
}

// TestDefault_ReturnsAFreshMap is the property that makes extending safe.
// Default is documented as returning a new map each call precisely so a caller
// adding "vault" cannot mutate the set every other caller sees. Returning a
// package-level map would satisfy every other test in this file while
// introducing exactly that cross-talk.
func TestDefault_ReturnsAFreshMap(t *testing.T) {
	first := Default()
	first["vault"] = stubBackend{}

	second := Default()
	if _, leaked := second["vault"]; leaked {
		t.Fatal("Default() shares one map between calls: a caller adding a scheme " +
			"would change the built-in set for everyone else")
	}
	if len(second) != 6 {
		t.Errorf("second Default() has %d backends, want the unmodified 6", len(second))
	}
}

// TestNewDefaultResolver_ResolvesABuiltIn checks the convenience constructor is
// wired to Default rather than to an empty set.
func TestNewDefaultResolver_ResolvesABuiltIn(t *testing.T) {
	t.Setenv("CLOUDSTIC_TEST_SECRET", "resolved-value")

	got, err := NewDefaultResolver().Resolve(context.Background(), "env://CLOUDSTIC_TEST_SECRET")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "resolved-value" {
		t.Errorf("Resolve = %q, want %q", got, "resolved-value")
	}
}

// TestExtendingDefault is the documented use case: add a scheme without losing
// the built-ins. This is what keeping the backends internal would have made
// impossible.
func TestExtendingDefault(t *testing.T) {
	t.Setenv("CLOUDSTIC_TEST_SECRET", "from-env")

	b := Default()
	b["vault"] = stubBackend{}
	resolver := secretref.NewResolver(b)

	if got, err := resolver.Resolve(context.Background(), "vault://team/prod"); err != nil {
		t.Fatalf("custom scheme: %v", err)
	} else if got != "from-stub" {
		t.Errorf("custom scheme = %q, want %q", got, "from-stub")
	}

	// The built-ins must still work alongside it.
	if got, err := resolver.Resolve(context.Background(), "env://CLOUDSTIC_TEST_SECRET"); err != nil {
		t.Fatalf("built-in scheme alongside a custom one: %v", err)
	} else if got != "from-env" {
		t.Errorf("built-in scheme = %q, want %q", got, "from-env")
	}
}

type stubBackend struct{}

func (stubBackend) Resolve(context.Context, secretref.Ref) (string, error) {
	return "from-stub", nil
}
