package main

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

func newTestLocalStore(t *testing.T) *localstore.Store {
	t.Helper()
	s, err := localstore.New(t.TempDir())
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
	if _, ok := s.(*localstore.Store); !ok {
		t.Fatalf("expected *localstore.Store, got %T", s)
	}
}

func TestNewObjectStore_UnknownScheme(t *testing.T) {
	if _, err := newObjectStore(context.Background(), storeConfig{uri: "nope:whatever"}); err == nil {
		t.Fatal("expected error for unknown store scheme")
	}
}

// B2 credentials come from the resolved b2Config, not from raw os.Getenv
// calls, so a missing credential is caught before any network dial is
// attempted.
func TestNewObjectStore_B2_RequiresCredentials(t *testing.T) {
	cases := []storeConfig{
		{uri: "b2:my-bucket"},
		{uri: "b2:my-bucket", b2: b2Config{keyID: "id-only"}},
		{uri: "b2:my-bucket", b2: b2Config{appKey: "key-only"}},
	}
	for _, cfg := range cases {
		if _, err := newObjectStore(context.Background(), cfg); err == nil {
			t.Fatalf("expected error for incomplete B2 credentials: %+v", cfg.b2)
		}
	}
}
