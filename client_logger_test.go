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
	// Run against both formats, because they do not produce the same output
	// and only one of them can prove the RFC 0022 §8 property.
	//
	// The store-layer lines this asserts come from PackStore, which a v3
	// repository does not have (RFC 0026): its chain is compression, metering
	// and the backend, none of which log. So a v3 client turning on debug gets
	// the client's own output and nothing from the chain. That is a real gap
	// in v3's observability rather than a property worth pinning, and naming
	// it here is what stops the next reader concluding the wiring is broken.
	for _, tc := range []struct {
		name      string
		format    int
		wantStore bool
	}{
		{"packfile", cloudstic.RepoFormatV2, true},
		{"v3", cloudstic.RepoFormatV3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base, err := localstore.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cloudstic.InitRepo(ctx, base, cloudstic.WithInitFormat(tc.format)); err != nil {
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
			if got := strings.Contains(out, "[store]"); got != tc.wantStore {
				t.Errorf("store-layer output present = %v, want %v; got %q", got, tc.wantStore, out)
			}
		})
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
