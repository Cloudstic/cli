package main

import (
	"context"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
)

// This file layers a store, keychain, and reporter into a repository client,
// from resolved configuration. See storebuild.go for the store layer alone.

// openClient constructs the object store described by cfg and opens a
// repository client on top of it. Passing a nil reporter selects one from the
// configured output mode.
func openClient(ctx context.Context, cfg clientConfig, reporterOverride cloudstic.Reporter) (*cloudstic.Client, error) {
	raw, err := newObjectStore(ctx, cfg.store)
	if err != nil {
		return nil, err
	}
	raw, debugLog := withDebugStore(raw, cfg.store.debug)

	reporter := reporterOverride
	if reporter == nil {
		reporter = newReporter(cfg, debugLog)
	}

	kc, err := buildKeychain(ctx, cfg.unlock)
	if err != nil {
		return nil, err
	}

	return cloudstic.NewClient(ctx, raw,
		cloudstic.WithKeychain(kc),
		cloudstic.WithReporter(reporter),
		cloudstic.WithPackfile(!cfg.disablePackfile),
	)
}

func newReporter(cfg clientConfig, debugLog *ui.SafeLogWriter) cloudstic.Reporter {
	if cfg.quiet || cfg.json {
		return ui.NewNoOpReporter()
	}
	cr := ui.NewConsoleReporter()
	if debugLog != nil {
		cr.SetLogWriter(debugLog)
	}
	return cr
}
