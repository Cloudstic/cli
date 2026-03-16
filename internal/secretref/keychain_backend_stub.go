//go:build !darwin || (darwin && !cgo)

package secretref

import (
	"context"
	"fmt"
)

func defaultKeychainLookup(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("%w: keychain backend is only available on macOS", errKeychainUnavailable)
}
