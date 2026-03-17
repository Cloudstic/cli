//go:build !darwin || (darwin && !cgo)

package secretref

import (
	"context"
	"fmt"
)

func defaultKeychainLookup(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("%w: keychain backend is only available on macOS", errKeychainUnavailable)
}

func defaultKeychainExists(_ context.Context, _, _ string) (bool, error) {
	return false, fmt.Errorf("%w: keychain backend is only available on macOS", errKeychainUnavailable)
}

func defaultKeychainStore(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("%w: keychain backend is only available on macOS", errKeychainUnavailable)
}
