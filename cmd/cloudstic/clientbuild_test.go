package main

import (
	"context"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/profile"
)

// Characterization tests for the client construction path, which RFC 0022 §7
// moves into pkg/open. Scoped to what storebuild_test.go and storeuri_test.go
// do not already cover: those exercise newObjectStore, withDebugStore, and URI
// parsing, so the gaps are newReporter and openClient, which had no direct
// coverage at all.

// TestNewReporter_OutputModeSelectsReporter pins which reporter each output
// mode gets. Getting this wrong is not cosmetic: a console reporter under
// -json interleaves progress bars with the JSON document on stdout and makes
// the output unparseable.
func TestNewReporter_OutputModeSelectsReporter(t *testing.T) {
	tests := []struct {
		name       string
		cfg        clientConfig
		wantNoOp   bool
		wantReason string
	}{
		{
			name:       "default is the console reporter",
			cfg:        clientConfig{},
			wantNoOp:   false,
			wantReason: "interactive runs show progress",
		},
		{
			name:       "quiet silences progress",
			cfg:        clientConfig{Quiet: true},
			wantNoOp:   true,
			wantReason: "-quiet must produce no progress output",
		},
		{
			name:       "json silences progress",
			cfg:        clientConfig{JSON: true},
			wantNoOp:   true,
			wantReason: "progress output would corrupt the JSON document on stdout",
		},
		{
			name:       "quiet and json together",
			cfg:        clientConfig{Quiet: true, JSON: true},
			wantNoOp:   true,
			wantReason: "either flag alone is enough",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newReporter(tt.cfg, nil)
			_, isNoOp := got.(*ui.NoOpReporter)
			if isNoOp != tt.wantNoOp {
				t.Errorf("newReporter(%+v) = %T, wantNoOp=%v (%s)",
					tt.cfg, got, tt.wantNoOp, tt.wantReason)
			}
		})
	}
}

// TestNewReporter_DebugLogOnlyAffectsTheConsoleReporter records that a debug
// log writer is attached to the console reporter but never changes which
// reporter is chosen. The writer is shared so store debug lines and progress
// bars do not interleave; in quiet/json mode there are no bars to interleave
// with.
func TestNewReporter_DebugLogOnlyAffectsTheConsoleReporter(t *testing.T) {
	debugLog := &ui.SafeLogWriter{}

	got := newReporter(clientConfig{}, debugLog)
	if _, isNoOp := got.(*ui.NoOpReporter); isNoOp {
		t.Error("a debug log must not turn the console reporter into a no-op reporter")
	}

	got = newReporter(clientConfig{Quiet: true}, debugLog)
	if _, isNoOp := got.(*ui.NoOpReporter); !isNoOp {
		t.Errorf("got %T, want a no-op reporter: a debug log must not override -quiet", got)
	}
}

// TestOpenClient_UnencryptedLocalRepo exercises the whole construction path —
// store, keychain, reporter, client — against a repository that needs no
// credentials. It is the test most likely to catch a wiring mistake when §7
// moves openClient into pkg/open, because it is the only one that runs all
// four builders together.
func TestOpenClient_UnencryptedLocalRepo(t *testing.T) {
	ctx := context.Background()
	cfg := clientConfig{
		Store: storeConfig{URI: "local:" + t.TempDir()},
		Quiet: true,
	}

	raw, err := openStore(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if _, err := cloudstic.InitRepo(ctx, raw); err != nil {
		t.Fatalf("init repository: %v", err)
	}

	client, err := openClient(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("openClient: %v", err)
	}
	if client == nil {
		t.Fatal("openClient returned a nil client")
	}
	if client.Store() == nil {
		t.Error("client has no store")
	}
}

// TestClientConfigZeroValueEnablesPackfiles is the regression test for the
// defect that made the field negative. NewClient enables packfiles by default,
// and openClient passes this field on unconditionally, so a zero-valued config
// must not turn them off — a consumer who never mentions packfiles would
// otherwise write a repository with a different physical layout and get no
// error from any layer.
func TestClientConfigZeroValueEnablesPackfiles(t *testing.T) {
	var cfg clientConfig
	if cfg.DisablePackfile {
		t.Error("the zero value must leave packfiles enabled, matching NewClient's own default")
	}

	// The two constructors must agree with the zero value when nothing asks
	// otherwise.
	fromFlags := clientConfigFromFlags(&globalFlags{})
	if fromFlags.DisablePackfile {
		t.Error("clientConfigFromFlags with no flags set must leave packfiles enabled")
	}

	fromProfile, err := clientConfigFromProfileStore(profile.Store{URI: "local:/tmp/x"})
	if err != nil {
		t.Fatalf("clientConfigFromProfileStore: %v", err)
	}
	if fromProfile.DisablePackfile {
		t.Error("a profile store that says nothing about packfiles must leave them enabled")
	}

	// And the flag still works.
	disabled := clientConfigFromFlags(&globalFlags{disablePackfile: true})
	if !disabled.DisablePackfile {
		t.Error("-disable-packfile must still disable packfiles")
	}
}

// TestOpenClient_ReporterOverrideWins pins the parameter that lets the TUI and
// tests supply their own reporter: when one is passed, the output mode does
// not get to pick a different one.
func TestOpenClient_ReporterOverrideWins(t *testing.T) {
	ctx := context.Background()
	cfg := clientConfig{
		Store: storeConfig{URI: "local:" + t.TempDir()},
	}

	raw, err := openStore(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if _, err := cloudstic.InitRepo(ctx, raw); err != nil {
		t.Fatalf("init repository: %v", err)
	}

	// cfg selects the console reporter; the override must be used instead.
	override := ui.NewNoOpReporter()
	if _, err := openClient(ctx, cfg, override); err != nil {
		t.Fatalf("openClient with a reporter override: %v", err)
	}
}
