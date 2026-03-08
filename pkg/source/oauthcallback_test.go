package source

import (
	"encoding/hex"
	"testing"
)

func TestRandomState_Length(t *testing.T) {
	state, err := randomState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 16 bytes = 32 hex chars
	if len(state) != 32 {
		t.Errorf("expected 32 hex chars, got %d: %q", len(state), state)
	}
	// Must be valid hex.
	if _, err := hex.DecodeString(state); err != nil {
		t.Errorf("state is not valid hex: %v", err)
	}
}

func TestRandomState_Unique(t *testing.T) {
	s1, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	if s1 == s2 {
		t.Error("two calls to randomState should produce different values")
	}
}
