package backends

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/secretref"
)

func configTokenRef(t *testing.T) secretref.Ref {
	t.Helper()
	ref, err := secretref.Parse("config-token://google/default")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return ref
}

// WithConfigDir is what makes this backend usable outside the CLI. Without it
// the managed directory comes from paths.ConfigDir, so an embedding program
// would write its users' tokens into Cloudstic's own config directory with no
// way to say otherwise.
func TestConfigTokenBackend_WithConfigDir_WritesThere(t *testing.T) {
	dir := t.TempDir()
	b := NewConfigTokenBackend(WithConfigDir(dir))
	ref := configTokenRef(t)
	ctx := context.Background()

	if err := b.Store(ctx, ref, "a-token"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := b.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "a-token" {
		t.Errorf("Resolve = %q, want %q", got, "a-token")
	}

	// It must have landed under the directory we chose, not the ambient one.
	var found []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			found = append(found, strings.TrimPrefix(p, dir))
		}
		return nil
	})
	if len(found) == 0 {
		t.Fatal("nothing was written under the configured directory")
	}
}

// The at-rest key is derived from a salt stored in the managed directory, so
// two backends pointed at different directories must not be able to read each
// other's tokens. This is the property that makes WithConfigDir a real
// isolation boundary rather than just a path preference.
func TestConfigTokenBackend_DirectoriesAreIsolated(t *testing.T) {
	ctx := context.Background()
	ref := configTokenRef(t)

	first := NewConfigTokenBackend(WithConfigDir(t.TempDir()))
	if err := first.Store(ctx, ref, "secret-one"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	second := NewConfigTokenBackend(WithConfigDir(t.TempDir()))
	if _, err := second.Resolve(ctx, ref); err == nil {
		t.Error("a backend in a different directory must not resolve the other's token")
	}
}

// An unconfigured backend keeps its previous behaviour: the managed directory
// comes from the environment, which is what the CLI relies on.
func TestConfigTokenBackend_DefaultsToAmbientConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", dir)
	ctx := context.Background()
	ref := configTokenRef(t)

	b := NewConfigTokenBackend()
	if err := b.Store(ctx, ref, "ambient"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := b.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "ambient" {
		t.Errorf("Resolve = %q, want %q", got, "ambient")
	}
}
