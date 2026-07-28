//go:build linux

package backends

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/cloudstic/cli/pkg/secretref"
)

func TestSecretServiceBackendIntegration(t *testing.T) {
	raw := os.Getenv("CLOUDSTIC_TEST_SECRET_SERVICE_REF")
	want := os.Getenv("CLOUDSTIC_TEST_SECRET_SERVICE_VALUE")
	if raw == "" || want == "" {
		t.Skip("set CLOUDSTIC_TEST_SECRET_SERVICE_REF and CLOUDSTIC_TEST_SECRET_SERVICE_VALUE to run Secret Service integration test")
	}

	parsed, err := secretref.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b := NewSecretServiceBackend()
	got, err := b.Resolve(context.Background(), parsed)
	if err != nil {
		var refErr *secretref.Error
		if errors.As(err, &refErr) && refErr.Kind == secretref.KindBackendUnavailable {
			t.Skipf("secret service backend unavailable: %v", err)
		}
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Fatalf("Resolve: got %q want %q", got, want)
	}
}
