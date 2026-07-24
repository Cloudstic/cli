package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/cloudstic/cli/pkg/store"
)

// exitCrashInjected is the process exit code used when
// CLOUDSTIC_TEST_CRASH_AFTER_PUTS triggers a simulated crash. It is distinct
// from exitFailure so a test can tell "the crash fired" apart from "the
// command failed for an unrelated reason".
const exitCrashInjected = 137

// withCrashInjection wraps s so the process hard-exits — skipping every
// deferred cleanup and buffered flush, exactly like a real crash — right
// after the Nth successful backend Put, when CLOUDSTIC_TEST_CRASH_AFTER_PUTS
// is set. It is a no-op otherwise.
//
// This is a test-only knob (see AGENTS.md's CLOUDSTIC_TEST_* convention),
// compiled into every build but inert unless that variable is set. It exists
// so e2e crash-consistency tests can kill the real binary at a precise,
// reproducible point in a backup rather than racing an external signal.
func withCrashInjection(s store.ObjectStore) (store.ObjectStore, error) {
	raw := os.Getenv("CLOUDSTIC_TEST_CRASH_AFTER_PUTS")
	if raw == "" {
		return s, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("CLOUDSTIC_TEST_CRASH_AFTER_PUTS: invalid count %q", raw)
	}
	cs := &crashAfterPutsStore{ObjectStore: s}
	cs.remaining.Store(int64(n))
	return cs, nil
}

// crashAfterPutsStore hard-exits the process after its Nth successful Put.
// Wrapping the raw backend (below compression/encryption/packing) means the
// count reflects real physical writes to the backend, which is what a
// crash-consistency test needs to control.
type crashAfterPutsStore struct {
	store.ObjectStore
	remaining atomic.Int64
}

func (s *crashAfterPutsStore) Unwrap() store.ObjectStore { return s.ObjectStore }

func (s *crashAfterPutsStore) Put(ctx context.Context, key string, data []byte) error {
	if err := s.ObjectStore.Put(ctx, key, data); err != nil {
		return err
	}
	if s.remaining.Add(-1) == 0 {
		os.Exit(exitCrashInjected)
	}
	return nil
}
