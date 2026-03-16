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

	_, err := defaultKeychainLookup(context.Background(), "svc", "acct")
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

	_, err := defaultKeychainLookup(context.Background(), "svc", "acct")
	if !errors.Is(err, errKeychainUnavailable) {
		t.Fatalf("expected errKeychainUnavailable, got %v", err)
	}
}
