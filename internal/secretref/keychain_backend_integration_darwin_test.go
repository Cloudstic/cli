//go:build darwin

package secretref

import (
	"context"
	"os"
	"os/exec"
	"strconv"
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
		t.Fatalf("Resolve: %v", err)
	}
	if got != secret {
		t.Fatalf("Resolve: got %q want %q", got, secret)
	}
}
