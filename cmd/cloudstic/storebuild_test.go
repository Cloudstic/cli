package main

import (
	"context"
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

func TestWithDebugStore_Disabled(t *testing.T) {
	logger.Writer = nil

	inner := newTestLocalStore(t)
	result, log := withDebugStore(inner, false)

	if result != inner {
		t.Error("Expected withDebugStore to return the original store when debug is false")
	}
	if log != nil {
		t.Error("Expected no log writer when debug is false")
	}
	if logger.Writer != nil {
		t.Error("Expected logger.Writer to remain nil when debug is false")
	}
}

func TestWithDebugStore_Enabled(t *testing.T) {
	logger.Writer = nil
	defer func() { logger.Writer = nil }()

	inner := newTestLocalStore(t)
	result, log := withDebugStore(inner, true)

	if _, ok := result.(*store.DebugStore); !ok {
		t.Errorf("Expected result to be *store.DebugStore, got %T", result)
	}
	if log == nil {
		t.Error("Expected a log writer when debug is true")
	}
	if logger.Writer == nil {
		t.Error("Expected logger.Writer to be set when debug is true")
	}
}

// Store construction takes a config value, so it is exercised without going
// through flag parsing at all.
func TestNewObjectStore_FromConfigWithoutFlagParsing(t *testing.T) {
	dir := t.TempDir()
	s, err := newObjectStore(context.Background(), storeConfig{uri: "local:" + dir})
	if err != nil {
		t.Fatalf("newObjectStore: %v", err)
	}
	if _, ok := s.(*store.LocalStore); !ok {
		t.Fatalf("expected *store.LocalStore, got %T", s)
	}
}

func TestNewObjectStore_UnknownScheme(t *testing.T) {
	if _, err := newObjectStore(context.Background(), storeConfig{uri: "nope:whatever"}); err == nil {
		t.Fatal("expected error for unknown store scheme")
	}
}
