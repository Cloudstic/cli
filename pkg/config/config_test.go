package config_test

import (
	"testing"

	"github.com/cloudstic/cli/pkg/config"
)

// TestZeroValueIsTheCorrectDefault is the contract that makes these structs
// safe to hand to a caller who fills in only the fields they care about.
//
// It is not a tautology about Go zero values: it is the reason DisablePackfile
// is spelled negatively. The client enables packfiles by default, and pkg/open
// passes this field on unconditionally, so a positive `Packfile bool` would
// mean that every caller writing config.Client{Store: …} silently got a
// repository with a different physical layout — with no error from any layer.
// If someone later "tidies" the field into the positive form, this test is
// what should stop them.
func TestZeroValueIsTheCorrectDefault(t *testing.T) {
	var c config.Client

	if c.DisablePackfile {
		t.Error("the zero value must leave packfiles enabled, matching the client's own default")
	}
	if c.Quiet || c.JSON {
		t.Error("the zero value must select the default output mode, not a silent one")
	}
	if c.Unlock.NoPrompt {
		t.Error("the zero value must not suppress the interactive fallback")
	}
	if c.Unlock.Prompt {
		t.Error("the zero value must not force an interactive prompt either; both are opt-in")
	}
	if c.Store.Debug {
		t.Error("the zero value must not enable debug output")
	}
	if c.Store.SFTP.Insecure {
		t.Error("the zero value must not disable SFTP host-key verification")
	}
}

// TestClientIsComparable records that these are plain value types: no maps,
// slices, or pointers. Callers can compare two configurations with == and copy
// one by assignment, which is what lets the CLI resolve a configuration per
// profile without any aliasing between them.
func TestClientIsComparable(t *testing.T) {
	a := config.Client{Store: config.Store{URI: "local:/tmp/x"}}
	b := a

	if a != b {
		t.Fatal("a copied configuration must equal its original")
	}

	b.Store.URI = "s3:bucket"
	if a.Store.URI != "local:/tmp/x" {
		t.Error("mutating a copy must not affect the original; a reference field has crept in")
	}
}
