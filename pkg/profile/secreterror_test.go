package profile

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/pkg/secretref"
)

// A malformed secret reference in a profile is rejected during validation, and
// the failure must stay inspectable rather than collapsing into an opaque
// string: callers branch on Kind to tell "you typed the scheme wrong" from
// "that backend is unavailable on this machine".
//
// This lived in the root package's tests, where the reasoning was that
// secretref was internal and errors.As on the re-exported alias was a caller's
// only handle on it. secretref is public now, so the alias is no longer what
// makes this reachable — but the wrapping through Save still has to preserve
// the typed error, which is what this pins.
func TestSave_SecretRefErrorIsInspectable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	cfg := &Config{
		Version: 1,
		Stores: map[string]Store{
			"prod": {URI: "local:./store", PasswordSecret: "invalid"},
		},
	}

	err := Save(path, cfg)
	if err == nil {
		t.Fatal("Save: expected a validation error for a malformed secret reference")
	}

	var refErr *secretref.Error
	if !errors.As(err, &refErr) {
		t.Fatalf("errors.As(err, *secretref.Error) failed on: %v", err)
	}
	if refErr.Kind != secretref.KindInvalidRef {
		t.Errorf("Kind = %v, want %v", refErr.Kind, secretref.KindInvalidRef)
	}
}
