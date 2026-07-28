package backends

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudstic/cli/pkg/secretref"
)

// TestEnvBackend_RejectsInvalidName covers validation that used to be asserted
// through the resolver in the contract package's tests. It is backend
// behaviour, not contract behaviour, so it moved here with the backend: an
// env:// path must be a legal shell variable name, and anything else is a
// malformed reference rather than a missing value.
func TestEnvBackend_RejectsInvalidName(t *testing.T) {
	b := NewEnvBackend(func(string) (string, bool) { return "", false })

	_, err := b.Resolve(context.Background(), secretref.Ref{Raw: "env://bad-name", Scheme: "env", Path: "bad-name"})
	if err == nil {
		t.Fatal("expected an error for an invalid env variable name")
	}
	var refErr *secretref.Error
	if !errors.As(err, &refErr) {
		t.Fatalf("expected *secretref.Error, got %T", err)
	}
	if refErr.Kind != secretref.KindInvalidRef {
		t.Errorf("Kind = %s, want %s", refErr.Kind, secretref.KindInvalidRef)
	}
}
