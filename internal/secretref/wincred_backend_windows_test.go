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
