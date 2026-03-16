//go:build darwin

package secretref

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/keybase/go-keychain"
)

func TestKeychainBackend_Integration(t *testing.T) {
	if os.Getenv("CLOUDSTIC_TEST_KEYCHAIN") != "1" {
		t.Skip("set CLOUDSTIC_TEST_KEYCHAIN=1 to run keychain integration test")
	}

	service := "cloudstic-test-service-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	account := "cloudstic-test-account"
	secret := "cloudstic-test-secret"

	item := keychain.NewGenericPassword(service, account, "", []byte(secret), "")
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	if err := keychain.AddItem(item); err != nil {
		if err == keychain.ErrorInteractionNotAllowed || err == keychain.ErrorNotAvailable || err == keychain.ErrorNoSuchKeychain {
			t.Skipf("keychain unavailable in this session: %v", err)
		}
		if err == keychain.ErrorDuplicateItem {
			query := keychain.NewItem()
			query.SetSecClass(keychain.SecClassGenericPassword)
			query.SetService(service)
			query.SetAccount(account)
			update := keychain.NewItem()
			update.SetData([]byte(secret))
			if updateErr := keychain.UpdateItem(query, update); updateErr != nil {
				t.Fatalf("update duplicate keychain item: %v", updateErr)
			}
		} else {
			t.Fatalf("add keychain item failed: %v", err)
		}
	}
	t.Cleanup(func() {
		_ = keychain.DeleteGenericPasswordItem(service, account)
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
