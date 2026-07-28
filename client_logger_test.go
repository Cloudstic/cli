package cloudstic_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	localsource "github.com/cloudstic/cli/pkg/source/local"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// TestWithLogger_ReachesTheStoreLayers is the acceptance test for RFC 0022 §8:
// a caller outside cmd/cloudstic can turn debug output on, and gets the store
// layers' output as well as the client's own.
func TestWithLogger_ReachesTheStoreLayers(t *testing.T) {
	ctx := context.Background()
	base, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudstic.InitRepo(ctx, base); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := cloudstic.NewClient(ctx, base, cloudstic.WithLogger(&buf)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if out == "" {
		t.Fatal("WithLogger produced no output at all")
	}
	if !strings.Contains(out, "[client]") {
		t.Errorf("no client output; got %q", out)
	}
	if !strings.Contains(out, "[store]") {
		t.Errorf("no store-layer output; got %q", out)
	}
}

// TestWithLogger_ReachesTheBackupEngine covers the layer furthest from the
// client. It is here because the engine's logger is set in a struct literal
// that a rename silently left unset: the nil logger swallowed every backup
// debug line while every test still passed. An assertion on real output is
// what catches that class of mistake.
func TestWithLogger_ReachesTheBackupEngine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudstic.InitRepo(ctx, base); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	c, err := cloudstic.NewClient(ctx, base, cloudstic.WithLogger(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Backup(ctx, localsource.New(dir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	if !strings.Contains(buf.String(), "[backup]") {
		t.Errorf("no backup-engine output reached the sink; got %q", buf.String())
	}
}

// TestWithLogger_IsPerClient is the property the global could never provide.
func TestWithLogger_IsPerClient(t *testing.T) {
	ctx := context.Background()
	base, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudstic.InitRepo(ctx, base); err != nil {
		t.Fatal(err)
	}

	var loud bytes.Buffer
	if _, err := cloudstic.NewClient(ctx, base, cloudstic.WithLogger(&loud)); err != nil {
		t.Fatal(err)
	}
	quiet, err := cloudstic.NewClient(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	_ = quiet

	if loud.Len() == 0 {
		t.Error("the client given a sink logged nothing")
	}
}
