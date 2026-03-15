package secretref

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	errKeychainNotFound    = errors.New("keychain item not found")
	errKeychainUnavailable = errors.New("keychain backend unavailable")
)

type keychainLookupFunc func(ctx context.Context, service, account string) (string, error)

// KeychainBackend resolves keychain://service/account references.
type KeychainBackend struct {
	lookup keychainLookupFunc
}

// NewKeychainBackend creates a keychain backend for the current platform.
func NewKeychainBackend() *KeychainBackend {
	return &KeychainBackend{lookup: defaultKeychainLookup}
}

func newKeychainBackendWithLookup(lookup keychainLookupFunc) *KeychainBackend {
	if lookup == nil {
		lookup = defaultKeychainLookup
	}
	return &KeychainBackend{lookup: lookup}
}

func parseKeychainPath(path string) (service string, account string, err error) {
	p := strings.TrimSpace(path)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", "", errors.New("empty keychain path")
	}
	i := strings.LastIndex(p, "/")
	if i <= 0 || i == len(p)-1 {
		return "", "", errors.New("expected keychain://<service>/<account>")
	}
	service = strings.TrimSpace(p[:i])
	account = strings.TrimSpace(p[i+1:])
	if service == "" || account == "" {
		return "", "", errors.New("expected keychain://<service>/<account>")
	}
	return service, account, nil
}

func (b *KeychainBackend) Resolve(ctx context.Context, ref Ref) (string, error) {
	service, account, err := parseKeychainPath(ref.Path)
	if err != nil {
		return "", errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	value, err := b.lookup(ctx, service, account)
	if err != nil {
		switch {
		case errors.Is(err, errKeychainNotFound):
			return "", errorf(KindNotFound, ref.Raw, fmt.Sprintf("keychain item %q/%q not found", service, account), err)
		case errors.Is(err, errKeychainUnavailable):
			return "", errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		default:
			return "", errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		}
	}

	return value, nil
}
