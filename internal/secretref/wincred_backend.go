package secretref

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	errWincredNotFound    = errors.New("windows credential not found")
	errWincredUnavailable = errors.New("windows credential backend unavailable")
)

type wincredLookupFunc func(ctx context.Context, target string) (string, error)
type wincredExistsFunc func(ctx context.Context, target string) (bool, error)
type wincredStoreFunc func(ctx context.Context, target, value string) error

// WincredBackend resolves wincred://target references.
type WincredBackend struct {
	lookup wincredLookupFunc
	exists wincredExistsFunc
	store  wincredStoreFunc
}

// NewWincredBackend creates a Windows Credential Manager backend for the
// current platform.
func NewWincredBackend() *WincredBackend {
	return &WincredBackend{
		lookup: defaultWincredLookup,
		exists: defaultWincredExists,
		store:  defaultWincredStore,
	}
}

func newWincredBackendWithFns(lookup wincredLookupFunc, exists wincredExistsFunc, store wincredStoreFunc) *WincredBackend {
	if lookup == nil {
		lookup = defaultWincredLookup
	}
	if exists == nil {
		exists = defaultWincredExists
	}
	if store == nil {
		store = defaultWincredStore
	}
	return &WincredBackend{lookup: lookup, exists: exists, store: store}
}

func newWincredBackendWithLookup(lookup wincredLookupFunc) *WincredBackend {
	return newWincredBackendWithFns(lookup, nil, nil)
}

func parseWincredTarget(path string) (string, error) {
	target := strings.TrimSpace(path)
	target = strings.TrimPrefix(target, "/")
	if target == "" {
		return "", errors.New("expected wincred://<target>")
	}
	return target, nil
}

func (b *WincredBackend) Resolve(ctx context.Context, ref Ref) (string, error) {
	target, err := parseWincredTarget(ref.Path)
	if err != nil {
		return "", errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	value, err := b.lookup(ctx, target)
	if err != nil {
		switch {
		case errors.Is(err, errWincredNotFound):
			return "", errorf(KindNotFound, ref.Raw, fmt.Sprintf("windows credential %q not found", target), err)
		case errors.Is(err, errWincredUnavailable):
			return "", errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		default:
			return "", errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
		}
	}

	return value, nil
}

func (b *WincredBackend) Scheme() string { return "wincred" }

func (b *WincredBackend) DisplayName() string { return "Windows Credential Manager" }

func (b *WincredBackend) WriteSupported() bool { return defaultWincredWriteSupported() }

func (b *WincredBackend) DefaultRef(storeName, account string) string {
	return "wincred://cloudstic/store/" + storeName + "/" + account
}

func (b *WincredBackend) Exists(ctx context.Context, ref Ref) (bool, error) {
	target, err := parseWincredTarget(ref.Path)
	if err != nil {
		return false, errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	exists, err := b.exists(ctx, target)
	if err != nil {
		return false, errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
	}
	return exists, nil
}

func (b *WincredBackend) Store(ctx context.Context, ref Ref, value string) error {
	target, err := parseWincredTarget(ref.Path)
	if err != nil {
		return errorf(KindInvalidRef, ref.Raw, err.Error(), nil)
	}

	if err := b.store(ctx, target, value); err != nil {
		return errorf(KindBackendUnavailable, ref.Raw, err.Error(), err)
	}
	return nil
}
