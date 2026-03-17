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

// WincredBackend resolves wincred://target references.
type WincredBackend struct {
	lookup wincredLookupFunc
}

// NewWincredBackend creates a Windows Credential Manager backend for the
// current platform.
func NewWincredBackend() *WincredBackend {
	return &WincredBackend{lookup: defaultWincredLookup}
}

func newWincredBackendWithLookup(lookup wincredLookupFunc) *WincredBackend {
	if lookup == nil {
		lookup = defaultWincredLookup
	}
	return &WincredBackend{lookup: lookup}
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
