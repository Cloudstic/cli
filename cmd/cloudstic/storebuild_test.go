package main

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// Store construction itself is tested in pkg/open. What is left here is the
// part that stayed behind because it is specific to being a terminal program:
// creating the shared debug writer, and the global it still sets.

func TestNewDebugLog_Disabled(t *testing.T) {
	logger.Writer = nil

	if log := newDebugLog(false); log != nil {
		t.Error("expected no log writer when debug is off")
	}
	if logger.Writer != nil {
		t.Error("expected logger.Writer to remain nil when debug is off")
	}
}

// TestNewDebugLog_Enabled pins the side effect as much as the return value.
// newDebugLog sets the package-level logger.Writer, which is what lets the
// engine and store layers log at all — and is the global RFC 0022 §8 removes.
// Asserting on it here means that removal cannot happen silently.
func TestNewDebugLog_Enabled(t *testing.T) {
	logger.Writer = nil
	defer func() { logger.Writer = nil }()

	log := newDebugLog(true)
	if log == nil {
		t.Fatal("expected a log writer when debug is on")
	}
	if logger.Writer == nil {
		t.Error("expected logger.Writer to be set when debug is on")
	}
}

// TestOpenStore_WrapsWhenDebugIsConfigured checks the adapter wires the debug
// writer through to pkg/open, rather than creating one and dropping it.
func TestOpenStore_WrapsWhenDebugIsConfigured(t *testing.T) {
	logger.Writer = nil
	defer func() { logger.Writer = nil }()

	s, err := openStore(context.Background(), storeConfig{URI: "local:" + t.TempDir(), Debug: true})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if _, ok := s.(*store.DebugStore); !ok {
		t.Errorf("expected a *store.DebugStore when Debug is set, got %T", s)
	}
}

func TestOpenStore_UnwrappedByDefault(t *testing.T) {
	s, err := openStore(context.Background(), storeConfig{URI: "local:" + t.TempDir()})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if _, ok := s.(*store.DebugStore); ok {
		t.Error("expected no debug wrapping when Debug is unset")
	}
	if _, ok := s.(*localstore.Store); !ok {
		t.Errorf("expected the bare backend, got %T", s)
	}
}
