//go:build darwin

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/keybase/go-keychain"
)

func TestSaveSecretToNativeStore_Success(t *testing.T) {
	origAdd := keychainAddItem
	origUpdate := keychainUpdateItem
	defer func() {
		keychainAddItem = origAdd
		keychainUpdateItem = origUpdate
	}()

	addCalled := false
	updateCalled := false
	keychainAddItem = func(item keychain.Item) error {
		addCalled = true
		return nil
	}
	keychainUpdateItem = func(_, _ keychain.Item) error {
		updateCalled = true
		return nil
	}

	err := saveSecretToNativeStore(context.Background(), "cloudstic/store/prod", "password", "super-secret")
	if err != nil {
		t.Fatalf("saveSecretToNativeStore: %v", err)
	}
	if !addCalled {
		t.Fatal("expected add to be called")
	}
	if updateCalled {
		t.Fatal("did not expect update path on successful add")
	}
}

func TestSaveSecretToNativeStore_Failure(t *testing.T) {
	origAdd := keychainAddItem
	defer func() { keychainAddItem = origAdd }()

	keychainAddItem = func(keychain.Item) error {
		return keychain.ErrorNotAvailable
	}

	err := saveSecretToNativeStore(context.Background(), "svc", "acct", "secret")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "save secret in macOS keychain failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "-25291") {
		t.Fatalf("expected keychain status code in output: %v", err)
	}
}

func TestSaveSecretToNativeStore_DuplicateUpdates(t *testing.T) {
	origAdd := keychainAddItem
	origUpdate := keychainUpdateItem
	defer func() {
		keychainAddItem = origAdd
		keychainUpdateItem = origUpdate
	}()

	keychainAddItem = func(keychain.Item) error {
		return keychain.ErrorDuplicateItem
	}

	updated := false
	keychainUpdateItem = func(_, _ keychain.Item) error {
		updated = true
		return nil
	}

	if err := saveSecretToNativeStore(context.Background(), "svc", "acct", "new-secret"); err != nil {
		t.Fatalf("saveSecretToNativeStore: %v", err)
	}
	if !updated {
		t.Fatal("expected duplicate path to call update")
	}
}

func TestNativeSecretExists_Success(t *testing.T) {
	origGet := keychainGetGenericPassword
	defer func() { keychainGetGenericPassword = origGet }()

	keychainGetGenericPassword = func(service, account, label, accessGroup string) ([]byte, error) {
		if service != "cloudstic/store/prod" || account != "password" || label != "" || accessGroup != "" {
			return nil, keychain.ErrorParam
		}
		return []byte("secret"), nil
	}

	exists, err := nativeSecretExists(context.Background(), "cloudstic/store/prod", "password")
	if err != nil {
		t.Fatalf("nativeSecretExists: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
}

func TestNativeSecretExists_NotFound(t *testing.T) {
	origGet := keychainGetGenericPassword
	defer func() { keychainGetGenericPassword = origGet }()

	keychainGetGenericPassword = func(service, account, label, accessGroup string) ([]byte, error) {
		return nil, keychain.ErrorItemNotFound
	}

	exists, err := nativeSecretExists(context.Background(), "svc", "acct")
	if err != nil {
		t.Fatalf("nativeSecretExists: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false")
	}
}

func TestNativeSecretExists_NotFoundNilData(t *testing.T) {
	origGet := keychainGetGenericPassword
	defer func() { keychainGetGenericPassword = origGet }()

	keychainGetGenericPassword = func(service, account, label, accessGroup string) ([]byte, error) {
		return nil, nil
	}

	exists, err := nativeSecretExists(context.Background(), "svc", "acct")
	if err != nil {
		t.Fatalf("nativeSecretExists: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for nil data result")
	}
}

func TestNativeSecretExists_OtherError(t *testing.T) {
	origGet := keychainGetGenericPassword
	defer func() { keychainGetGenericPassword = origGet }()

	keychainGetGenericPassword = func(service, account, label, accessGroup string) ([]byte, error) {
		return nil, keychain.ErrorNotAvailable
	}

	exists, err := nativeSecretExists(context.Background(), "svc", "acct")
	if err == nil {
		t.Fatal("expected error")
	}
	if exists {
		t.Fatal("expected exists=false")
	}
	if !strings.Contains(err.Error(), "check secret in macOS keychain failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
