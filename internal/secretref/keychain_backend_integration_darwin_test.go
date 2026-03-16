//go:build darwin

package secretref

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestKeychainBackend_Integration(t *testing.T) {
	if os.Getenv("CLOUDSTIC_TEST_KEYCHAIN") != "1" {
		t.Skip("set CLOUDSTIC_TEST_KEYCHAIN=1 to run keychain integration test")
	}

	service := "cloudstic-test-service-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	account := "cloudstic-test-account"
	secret := "cloudstic-test-secret"

	add := exec.Command("security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", secret)
	if out, err := add.CombinedOutput(); err != nil {
		msg := strings.ToLower(strings.TrimSpace(string(out)))
		if strings.Contains(msg, "user interaction is not allowed") || strings.Contains(msg, "interaction not allowed") || strings.Contains(msg, "not allowed") {
			t.Skipf("keychain unavailable in this session: %s", strings.TrimSpace(string(out)))
		}
		t.Fatalf("add-generic-password failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		del := exec.Command("security", "delete-generic-password", "-s", service, "-a", account)
		_, _ = del.CombinedOutput()
	})

	b := NewKeychainBackend()
	got, err := b.Resolve(context.Background(), Ref{
		Raw:    "keychain://" + service + "/" + account,
		Scheme: "keychain",
		Path:   service + "/" + account,
	})
	if err != nil {
		var refErr *Error
		if errors.As(err, &refErr) && refErr.Kind == KindBackendUnavailable {
			t.Skipf("keychain backend unavailable: %v", err)
		}
		t.Fatalf("Resolve: %v", err)
	}
	if got != secret {
		t.Fatalf("Resolve: got %q want %q", got, secret)
	}
}
