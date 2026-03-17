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
type keychainExistsFunc func(ctx context.Context, service, account string) (bool, error)
type keychainStoreFunc func(ctx context.Context, service, account, value string) error

// KeychainBackend resolves keychain://service/account references.
type KeychainBackend struct {
	lookup keychainLookupFunc
	exists keychainExistsFunc
	store  keychainStoreFunc
}

// NewKeychainBackend creates a keychain backend for the current platform.
func NewKeychainBackend() *KeychainBackend {
	return &KeychainBackend{
		lookup: defaultKeychainLookup,
		exists: defaultKeychainExists,
		store:  defaultKeychainStore,
	}
}

func newKeychainBackendWithFns(lookup keychainLookupFunc, exists keychainExistsFunc, store keychainStoreFunc) *KeychainBackend {
	if lookup == nil {
		lookup = defaultKeychainLookup
	}
	if exists == nil {
		exists = defaultKeychainExists
	}
	if store == nil {
		store = defaultKeychainStore
	}
	return &KeychainBackend{lookup: lookup, exists: exists, store: store}
}

func newKeychainBackendWithLookup(lookup keychainLookupFunc) *KeychainBackend {
	return newKeychainBackendWithFns(lookup, nil, nil)
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

func (b *KeychainBackend) Scheme() string { return "keychain" }

func (b *KeychainBackend) DisplayName() string { return "macOS Keychain" }

func (b *KeychainBackend) DefaultRef(storeName, account string) string {
	service := "cloudstic/store/" + storeName
	return "keychain://" + service + "/" + account
}

func (b *KeychainBackend) Exists(ctx context.Context, ref Ref) (bool, error) {
	service, account, err := parseKeychainPath(ref.Path)
	if err != nil {
		return false, errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	exists, err := b.exists(ctx, service, account)
	if err != nil {
		switch {
		case errors.Is(err, errKeychainUnavailable):
			return false, errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		default:
			return false, errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		}
	}
	return exists, nil
}

func (b *KeychainBackend) Store(ctx context.Context, ref Ref, value string) error {
	service, account, err := parseKeychainPath(ref.Path)
	if err != nil {
		return errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	if err := b.store(ctx, service, account, value); err != nil {
		switch {
		case errors.Is(err, errKeychainUnavailable):
			return errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		default:
			return errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		}
	}
	return nil
}
