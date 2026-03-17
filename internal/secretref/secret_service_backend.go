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

// SecretServiceBackend resolves secret-service://collection/item references.
type SecretServiceBackend struct {
	lookup secretServiceLookupFunc
}

// NewSecretServiceBackend creates a Secret Service backend for the current
// platform.
func NewSecretServiceBackend() *SecretServiceBackend {
	return &SecretServiceBackend{lookup: defaultSecretServiceLookup}
}

func newSecretServiceBackendWithLookup(lookup secretServiceLookupFunc) *SecretServiceBackend {
	if lookup == nil {
		lookup = defaultSecretServiceLookup
	}
	return &SecretServiceBackend{lookup: lookup}
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
