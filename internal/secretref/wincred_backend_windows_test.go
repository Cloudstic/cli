//go:build windows

package secretref

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDefaultWincredLookupMapsNotFound(t *testing.T) {
	orig := wincredReadGenericCredential
	defer func() { wincredReadGenericCredential = orig }()

	wincredReadGenericCredential = func(string) (string, error) {
		return "", windows.ERROR_NOT_FOUND
	}

	_, err := defaultWincredLookup(context.Background(), "target")
	if !errors.Is(err, errWincredNotFound) {
		t.Fatalf("expected errWincredNotFound, got %v", err)
	}
}

func TestDefaultWincredLookupMapsNoSuchLogonSession(t *testing.T) {
	orig := wincredReadGenericCredential
	defer func() { wincredReadGenericCredential = orig }()

	wincredReadGenericCredential = func(string) (string, error) {
		return "", windows.ERROR_NO_SUCH_LOGON_SESSION
	}

	_, err := defaultWincredLookup(context.Background(), "target")
	if !errors.Is(err, errWincredUnavailable) {
		t.Fatalf("expected errWincredUnavailable, got %v", err)
	}
}

func TestDefaultWincredExistsMapsNotFound(t *testing.T) {
	orig := wincredReadGenericCredential
	defer func() { wincredReadGenericCredential = orig }()

	wincredReadGenericCredential = func(string) (string, error) {
		return "", windows.ERROR_NOT_FOUND
	}

	exists, err := defaultWincredExists(context.Background(), "target")
	if err != nil {
		t.Fatalf("defaultWincredExists: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false")
	}
}

func TestDefaultWincredStoreMapsNoSuchLogonSession(t *testing.T) {
	orig := wincredWriteGenericCredential
	defer func() { wincredWriteGenericCredential = orig }()

	wincredWriteGenericCredential = func(string, string) error {
		return windows.ERROR_NO_SUCH_LOGON_SESSION
	}

	err := defaultWincredStore(context.Background(), "target", "secret")
	if !errors.Is(err, errWincredUnavailable) {
		t.Fatalf("expected errWincredUnavailable, got %v", err)
	}
}
