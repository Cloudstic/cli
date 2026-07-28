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
	debugLog := newDebugLog(cfg.Store.Debug)

	reporter := reporterOverride
	if reporter == nil {
		reporter = newReporter(cfg, debugLog)
	}

	opts := append(storeOptions(debugLog),
		open.WithReporter(reporter),
		open.WithPasswordPrompt(passwordPrompts()),
	)
	if debugLog != nil {
		opts = append(opts, open.WithLogger(debugLog))
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
	return cr
}
