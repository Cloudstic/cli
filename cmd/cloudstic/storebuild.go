package main

import (
	"context"

	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/open"
	"github.com/cloudstic/cli/pkg/store"
)

// Store construction lives in pkg/open. What stays here is what is specific to
// being a terminal program: creating the debug writer that the console
// reporter also draws through, and the build-tagged crash-injection hook the
// e2e tests use (RFC 0022 §7).

// openStore constructs the object store described by cfg, with debug wrapping
// applied. Used by commands that operate on the store directly (init, key).
func openStore(ctx context.Context, cfg storeConfig) (store.ObjectStore, error) {
	s, _, err := openStoreWithDebugLog(ctx, cfg)
	return s, err
}

// openStoreWithDebugLog also returns the debug log writer, which the console
// reporter shares so that store logs and progress output do not interleave.
func openStoreWithDebugLog(ctx context.Context, cfg storeConfig) (store.ObjectStore, *ui.SafeLogWriter, error) {
	debugLog := newDebugLog(cfg.Debug)
	s, err := open.Store(ctx, cfg, storeOptions(debugLog)...)
	if err != nil {
		return nil, nil, err
	}
	return s, debugLog, nil
}

// newDebugLog returns the shared debug writer when debug output is enabled,
// and nil otherwise.
//
// It sets nothing global. The writer is handed to whatever needs it — the
// store via open.WithDebugWriter, the client and the layers it drives via
// open.WithLogger, the console reporter so its output does not interleave —
// which is what lets two commands, or two clients, log independently
// (RFC 0022 §8).
func newDebugLog(debug bool) *ui.SafeLogWriter {
	if !debug {
		return nil
	}
	return &ui.SafeLogWriter{}
}

// storeOptions builds the pkg/open options every command's store shares.
//
// The crash-injection hook wraps the backend rather than the finished store,
// which is the order it has always had: it counts writes reaching the backend,
// not writes entering the decorator chain.
func storeOptions(debugLog *ui.SafeLogWriter) []open.Option {
	opts := []open.Option{open.WithBackendWrapper(withCrashInjection)}
	if debugLog != nil {
		opts = append(opts, open.WithDebugWriter(debugLog))
	}
	return opts
}
