package main

import (
	"flag"
	"testing"

	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

func newTestLocalStore(t *testing.T) *store.LocalStore {
	t.Helper()
	s, err := store.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	return s
}

func TestApplyDebug_Disabled(t *testing.T) {
	logger.Writer = nil

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	_ = fs.Parse([]string{}) // --debug defaults to false

	inner := newTestLocalStore(t)
	result := g.applyDebug(inner)

	// Without --debug, the store should be returned as-is.
	if result != inner {
		t.Error("Expected applyDebug to return the original store when --debug is false")
	}
	if logger.Writer != nil {
		t.Error("Expected logger.Writer to remain nil when --debug is false")
	}
}

func TestApplyDebug_Enabled(t *testing.T) {
	logger.Writer = nil
	defer func() { logger.Writer = nil }()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	_ = fs.Parse([]string{"-debug"})

	inner := newTestLocalStore(t)
	result := g.applyDebug(inner)

	// With --debug, the store should be wrapped in a DebugStore.
	if result == inner {
		t.Error("Expected applyDebug to wrap the store when --debug is true")
	}
	if _, ok := result.(*store.DebugStore); !ok {
		t.Errorf("Expected result to be *store.DebugStore, got %T", result)
	}
	if logger.Writer == nil {
		t.Error("Expected logger.Writer to be set when --debug is true")
	}
	if g.debugLog == nil {
		t.Error("Expected globalFlags.debugLog to be initialized when --debug is true")
	}
}

func TestApplyDebug_NilDebugField(t *testing.T) {
	logger.Writer = nil

	g := &globalFlags{} // debug field is nil
	inner := newTestLocalStore(t)
	result := g.applyDebug(inner)

	// With nil debug pointer, the store should be returned as-is.
	if result != inner {
		t.Error("Expected applyDebug to return the original store when debug is nil")
	}
}
