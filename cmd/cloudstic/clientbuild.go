package main

import (
	"context"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/open"
)

// Client construction lives in pkg/open. What stays here is choosing the
// progress reporter, which is presentation rather than configuration, and so
// belongs to the program doing the presenting (RFC 0022 §7).

// openClient constructs the object store described by cfg and opens a
// repository client on top of it. Passing a nil reporter selects one from the
// configured output mode.
func openClient(ctx context.Context, cfg clientConfig, reporterOverride cloudstic.Reporter) (*cloudstic.Client, error) {
	// Two audiences, one destination. -debug traces every store operation;
	// -verbose asks the query operations for their progress detail, which they
	// write to the component logger because they have no phases to report
	// against (list, ls, diff and find are queries, not long-running work).
	// Both land in the same writer so their lines interleave cleanly above the
	// progress bar — but only -debug may switch on the store decorator, or
	// asking for detail would flood the output with per-object traces.
	diagnostics := newDebugLog(cfg.Store.Debug || cfg.Verbose)

	reporter := reporterOverride
	if reporter == nil {
		reporter = newReporter(cfg, diagnostics)
	}

	storeOpts := storeOptions(nil)
	if cfg.Store.Debug {
		storeOpts = storeOptions(diagnostics)
	}

	opts := append(storeOpts,
		open.WithReporter(reporter),
		open.WithPasswordPrompt(passwordPrompts()),
	)
	if diagnostics != nil {
		opts = append(opts, open.WithLogger(diagnostics))
	}
	return open.Client(ctx, cfg, opts...)
}

func newReporter(cfg clientConfig, debugLog *ui.SafeLogWriter) cloudstic.Reporter {
	if cfg.Quiet || cfg.JSON {
		return ui.NewNoOpReporter()
	}
	cr := ui.NewConsoleReporter()
	if debugLog != nil {
		cr.SetLogWriter(debugLog)
	}
	// One flag, one decision. Every operation used to carry its own verbose
	// option; they now report at a detail level and this chooses which levels
	// are shown.
	if cfg.Verbose {
		cr.SetDetail(ui.DetailVerbose)
	}
	return cr
}
