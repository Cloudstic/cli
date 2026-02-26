package store

import (
	"testing"
)

func TestB2Store_SDK(t *testing.T) {
	// Similarly, Blazer SDK connects to real B2.
	// Mocking the entire B2 API for the SDK to consume is complex.
	t.Skip("Skipping B2 SDK tests as they require real credentials")
}
