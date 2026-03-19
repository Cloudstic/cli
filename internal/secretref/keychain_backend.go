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

type keychainLookupBlobFunc func(ctx context.Context, service, account string) ([]byte, error)
type keychainExistsFunc func(ctx context.Context, service, account string) (bool, error)
type keychainStoreBlobFunc func(ctx context.Context, service, account string, value []byte) error
type keychainDeleteFunc func(ctx context.Context, service, account string) error

// KeychainBackend resolves keychain://service/account references.
type KeychainBackend struct {
	lookupBlob keychainLookupBlobFunc
	exists     keychainExistsFunc
	storeBlob  keychainStoreBlobFunc
	delete     keychainDeleteFunc
}

// NewKeychainBackend creates a keychain backend for the current platform.
func NewKeychainBackend() *KeychainBackend {
	return &KeychainBackend{
		lookupBlob: defaultKeychainLookupBlob,
		exists:     defaultKeychainExists,
		storeBlob:  defaultKeychainStoreBlob,
		delete:     defaultKeychainDelete,
	}
}

func newKeychainBackendWithFns(lookup keychainLookupBlobFunc, exists keychainExistsFunc, store keychainStoreBlobFunc, deleteFn keychainDeleteFunc) *KeychainBackend {
	if lookup == nil {
		lookup = defaultKeychainLookupBlob
	}
	if exists == nil {
		exists = defaultKeychainExists
	}
	if store == nil {
		store = defaultKeychainStoreBlob
	}
	if deleteFn == nil {
		deleteFn = defaultKeychainDelete
	}
	return &KeychainBackend{lookupBlob: lookup, exists: exists, storeBlob: store, delete: deleteFn}
}

func newKeychainBackendWithLookup(lookup keychainLookupBlobFunc) *KeychainBackend {
	return newKeychainBackendWithFns(lookup, nil, nil, nil)
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
	data, err := b.LoadBlob(ctx, ref)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func (b *KeychainBackend) LoadBlob(ctx context.Context, ref Ref) ([]byte, error) {
	service, account, err := parseKeychainPath(ref.Path)
	if err != nil {
		return nil, errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	value, err := b.lookupBlob(ctx, service, account)
	if err != nil {
		switch {
		case errors.Is(err, errKeychainNotFound):
			return nil, errorf(KindNotFound, ref.Raw, fmt.Sprintf("keychain item %q/%q not found", service, account), err)
		case errors.Is(err, errKeychainUnavailable):
			return nil, errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		default:
			return nil, errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		}
	}

	return value, nil
}

func (b *KeychainBackend) Scheme() string { return "keychain" }

func (b *KeychainBackend) DisplayName() string { return "macOS Keychain" }

func (b *KeychainBackend) WriteSupported() bool { return defaultKeychainWriteSupported() }

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
	return b.SaveBlob(ctx, ref, []byte(value))
}

func (b *KeychainBackend) SaveBlob(ctx context.Context, ref Ref, data []byte) error {
	service, account, err := parseKeychainPath(ref.Path)
	if err != nil {
		return errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	if err := b.storeBlob(ctx, service, account, data); err != nil {
		switch {
		case errors.Is(err, errKeychainUnavailable):
			return errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		default:
			return errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		}
	}
	return nil
}

func (b *KeychainBackend) DeleteBlob(ctx context.Context, ref Ref) error {
	service, account, err := parseKeychainPath(ref.Path)
	if err != nil {
		return errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	if err := b.delete(ctx, service, account); err != nil {
		switch {
		case errors.Is(err, errKeychainUnavailable):
			return errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		default:
			return errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		}
	}
	return nil
}
