package secretref

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	errSecretServiceNotFound    = errors.New("secret service item not found")
	errSecretServiceUnavailable = errors.New("secret service backend unavailable")
)

type secretServiceLookupFunc func(ctx context.Context, collection, item string) (string, error)
type secretServiceExistsFunc func(ctx context.Context, collection, item string) (bool, error)
type secretServiceStoreFunc func(ctx context.Context, collection, item, value string) error

// SecretServiceBackend resolves secret-service://collection/item references.
type SecretServiceBackend struct {
	lookup secretServiceLookupFunc
	exists secretServiceExistsFunc
	store  secretServiceStoreFunc
}

// NewSecretServiceBackend creates a Secret Service backend for the current
// platform.
func NewSecretServiceBackend() *SecretServiceBackend {
	return &SecretServiceBackend{
		lookup: defaultSecretServiceLookup,
		exists: defaultSecretServiceExists,
		store:  defaultSecretServiceStore,
	}
}

func newSecretServiceBackendWithFns(lookup secretServiceLookupFunc, exists secretServiceExistsFunc, store secretServiceStoreFunc) *SecretServiceBackend {
	if lookup == nil {
		lookup = defaultSecretServiceLookup
	}
	if exists == nil {
		exists = defaultSecretServiceExists
	}
	if store == nil {
		store = defaultSecretServiceStore
	}
	return &SecretServiceBackend{lookup: lookup, exists: exists, store: store}
}

func newSecretServiceBackendWithLookup(lookup secretServiceLookupFunc) *SecretServiceBackend {
	return newSecretServiceBackendWithFns(lookup, nil, nil)
}

func parseSecretServicePath(path string) (collection string, item string, err error) {
	p := strings.TrimSpace(path)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", "", errors.New("expected secret-service://<collection>/<item>")
	}
	parts := strings.SplitN(p, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", errors.New("expected secret-service://<collection>/<item>")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func (b *SecretServiceBackend) Resolve(ctx context.Context, ref Ref) (string, error) {
	collection, item, err := parseSecretServicePath(ref.Path)
	if err != nil {
		return "", errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	value, err := b.lookup(ctx, collection, item)
	if err != nil {
		switch {
		case errors.Is(err, errSecretServiceNotFound):
			return "", errorf(KindNotFound, ref.Raw, fmt.Sprintf("secret service item %q/%q not found", collection, item), err)
		case errors.Is(err, errSecretServiceUnavailable):
			return "", errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		default:
			return "", errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		}
	}

	return value, nil
}

func (b *SecretServiceBackend) Scheme() string { return "secret-service" }

func (b *SecretServiceBackend) DisplayName() string { return "Secret Service" }

func (b *SecretServiceBackend) WriteSupported() bool { return defaultSecretServiceWriteSupported() }

func (b *SecretServiceBackend) DefaultRef(storeName, account string) string {
	return "secret-service://cloudstic/" + storeName + "/" + account
}

func (b *SecretServiceBackend) Exists(ctx context.Context, ref Ref) (bool, error) {
	collection, item, err := parseSecretServicePath(ref.Path)
	if err != nil {
		return false, errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	exists, err := b.exists(ctx, collection, item)
	if err != nil {
		return false, errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
	}
	return exists, nil
}

func (b *SecretServiceBackend) Store(ctx context.Context, ref Ref, value string) error {
	collection, item, err := parseSecretServicePath(ref.Path)
	if err != nil {
		return errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	if err := b.store(ctx, collection, item, value); err != nil {
		return errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
	}
	return nil
}
