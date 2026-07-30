package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// StoreFor is the lookup that used to live unexported in cmd/cloudstic, so an
// external caller had to re-derive from the YAML that a profile's `store:` key
// indexes the top-level `stores:` map (RFC 0022 §7).

func TestConfig_StoreFor(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Stores: map[string]Store{
			"backblaze": {URI: "b2:my-bucket", B2KeyID: "id"},
		},
		Profiles: map[string]Profile{
			"docs":      {Source: "local:/docs", Store: "backblaze"},
			"storeless": {Source: "local:/docs"},
			"dangling":  {Source: "local:/docs", Store: "gone"},
		},
	}

	t.Run("resolves the referenced store", func(t *testing.T) {
		s, err := cfg.StoreFor("docs")
		if err != nil {
			t.Fatalf("StoreFor: %v", err)
		}
		if s == nil {
			t.Fatal("StoreFor returned no store for a profile that names one")
		}
		if s.URI != "b2:my-bucket" || s.B2KeyID != "id" {
			t.Errorf("StoreFor = %+v, want the backblaze entry", *s)
		}
	})

	// A profile may leave the store to whoever runs it, so "no store" is an
	// answer rather than a failure — the CLI fills it from -store in that case.
	t.Run("a profile naming no store is not an error", func(t *testing.T) {
		s, err := cfg.StoreFor("storeless")
		if err != nil {
			t.Fatalf("StoreFor: %v", err)
		}
		if s != nil {
			t.Errorf("StoreFor = %+v, want nil for a profile that names no store", *s)
		}
	})

	t.Run("a dangling store reference errors", func(t *testing.T) {
		_, err := cfg.StoreFor("dangling")
		if err == nil {
			t.Fatal("expected an error for a store reference that resolves to nothing")
		}
		// The message has to name both ends: which profile to go fix, and
		// which store entry is missing.
		for _, want := range []string{"dangling", "gone"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("an unknown profile errors", func(t *testing.T) {
		_, err := cfg.StoreFor("nope")
		if err == nil {
			t.Fatal("expected an error for an unknown profile")
		}
		if !strings.Contains(err.Error(), "nope") {
			t.Errorf("error %q does not name the profile asked for", err)
		}
	})
}

// A zero Config has nil maps. Indexing those is well-defined, so the lookup
// must report "unknown profile" rather than panicking — Load normalizes, but
// nothing stops a caller from building a Config literal.
func TestConfig_StoreForOnZeroConfig(t *testing.T) {
	if _, err := (&Config{}).StoreFor("anything"); err == nil {
		t.Fatal("expected an error from a Config with no profiles")
	}
}

func TestDefaultPath(t *testing.T) {
	dir := t.TempDir()
	got, err := DefaultPath(dir)
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, DefaultFilename); got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

// Resolving where profiles live must not create anything: help, completion and
// setup -dry-run all ask this question without intending a side effect.
func TestDefaultPath_CreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created-yet")
	if _, err := DefaultPath(dir); err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("DefaultPath created %q (stat err = %v)", dir, err)
	}
}
