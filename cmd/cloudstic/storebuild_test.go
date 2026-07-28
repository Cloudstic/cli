package main

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// Store construction itself is tested in pkg/open. What is left here is the
// part that stayed behind because it is specific to being a terminal program:
// creating the shared debug writer, and the global it still sets.

func TestNewDebugLog_Disabled(t *testing.T) {
	if log := newDebugLog(false); log != nil {
		t.Error("expected no log writer when debug is off")
	}
}

// TestNewDebugLog_Enabled asserts what replaced the side effect. newDebugLog
// used to set a package-level writer; now it only returns one, and the callers
// hand it to whatever needs it. A writer that is created and dropped would
// leave -debug silently producing nothing, which is what this guards.
func TestNewDebugLog_Enabled(t *testing.T) {
	if log := newDebugLog(true); log == nil {
		t.Fatal("expected a log writer when debug is on")
	}
}

// TestOpenStore_WrapsWhenDebugIsConfigured checks the adapter wires the debug
// writer through to pkg/open, rather than creating one and dropping it.
func TestOpenStore_WrapsWhenDebugIsConfigured(t *testing.T) {
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
