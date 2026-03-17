//go:build linux

package secretref

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestMapSecretServiceCallErrorServiceUnknown(t *testing.T) {
	err := mapSecretServiceCallError(dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}, "lookup secret")
	if !errors.Is(err, errSecretServiceUnavailable) {
		t.Fatalf("expected errSecretServiceUnavailable, got %v", err)
	}
}

func TestDefaultSecretServiceLookupMissingSessionBus(t *testing.T) {
	orig := secretServiceSessionBus
	defer func() { secretServiceSessionBus = orig }()

	secretServiceSessionBus = func() (secretServiceDBusConn, error) {
		return nil, errors.New("dbus session unavailable")
	}

	_, err := defaultSecretServiceLookup(context.Background(), "cloudstic", "prod/password")
	if !errors.Is(err, errSecretServiceUnavailable) {
		t.Fatalf("expected errSecretServiceUnavailable, got %v", err)
	}
}

func TestDefaultSecretServiceExistsNotFound(t *testing.T) {
	orig := secretServiceSessionBus
	defer func() { secretServiceSessionBus = orig }()

	secretServiceSessionBus = func() (secretServiceDBusConn, error) {
		return nil, errors.New("dbus session unavailable")
	}

	_, err := defaultSecretServiceExists(context.Background(), "cloudstic", "prod/password")
	if !errors.Is(err, errSecretServiceUnavailable) {
		t.Fatalf("expected errSecretServiceUnavailable, got %v", err)
	}
}
