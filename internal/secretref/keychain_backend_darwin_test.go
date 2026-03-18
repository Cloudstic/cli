//go:build darwin && cgo

package secretref

import (
	"context"
	"errors"
	"testing"

	"github.com/keybase/go-keychain"
)

func TestDefaultKeychainLookup_NotFoundOnNilData(t *testing.T) {
	orig := keychainGetGenericPasswordDarwin
	defer func() { keychainGetGenericPasswordDarwin = orig }()

	keychainGetGenericPasswordDarwin = func(service, account, label, accessGroup string) ([]byte, error) {
		return nil, nil
	}

	_, err := defaultKeychainLookupBlob(context.Background(), "svc", "acct")
	if !errors.Is(err, errKeychainNotFound) {
		t.Fatalf("expected errKeychainNotFound, got %v", err)
	}
}

func TestDefaultKeychainLookup_MapsInteractionNotAllowed(t *testing.T) {
	orig := keychainGetGenericPasswordDarwin
	defer func() { keychainGetGenericPasswordDarwin = orig }()

	keychainGetGenericPasswordDarwin = func(service, account, label, accessGroup string) ([]byte, error) {
		return nil, keychain.ErrorInteractionNotAllowed
	}

	_, err := defaultKeychainLookupBlob(context.Background(), "svc", "acct")
	if !errors.Is(err, errKeychainUnavailable) {
		t.Fatalf("expected errKeychainUnavailable, got %v", err)
	}
}

func TestDefaultKeychainStore_DuplicateUpdates(t *testing.T) {
	origAdd := keychainAddItemDarwin
	origUpdate := keychainUpdateItemDarwin
	defer func() {
		keychainAddItemDarwin = origAdd
		keychainUpdateItemDarwin = origUpdate
	}()

	keychainAddItemDarwin = func(keychain.Item) error {
		return keychain.ErrorDuplicateItem
	}
	updated := false
	keychainUpdateItemDarwin = func(_, _ keychain.Item) error {
		updated = true
		return nil
	}

	if err := defaultKeychainStoreBlob(context.Background(), "svc", "acct", []byte("secret")); err != nil {
		t.Fatalf("defaultKeychainStoreBlob: %v", err)
	}
	if !updated {
		t.Fatal("expected update on duplicate item")
	}
}

func TestDefaultKeychainExists_NotFound(t *testing.T) {
	orig := keychainGetGenericPasswordDarwin
	defer func() { keychainGetGenericPasswordDarwin = orig }()

	keychainGetGenericPasswordDarwin = func(service, account, label, accessGroup string) ([]byte, error) {
		return nil, keychain.ErrorItemNotFound
	}

	exists, err := defaultKeychainExists(context.Background(), "svc", "acct")
	if err != nil {
		t.Fatalf("defaultKeychainExists: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false")
	}
}
