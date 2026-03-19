//go:build !darwin || (darwin && !cgo)

package secretref

import (
	"context"
	"fmt"
)

func defaultKeychainWriteSupported() bool { return false }

func defaultKeychainLookupBlob(_ context.Context, _, _ string) ([]byte, error) {
	return nil, fmt.Errorf("%w: keychain backend is only available on macOS", errKeychainUnavailable)
}

func defaultKeychainExists(_ context.Context, _, _ string) (bool, error) {
	return false, fmt.Errorf("%w: keychain backend is only available on macOS", errKeychainUnavailable)
}

func defaultKeychainStoreBlob(_ context.Context, _, _ string, _ []byte) error {
	return fmt.Errorf("%w: keychain backend is only available on macOS", errKeychainUnavailable)
}

func defaultKeychainDelete(_ context.Context, _, _ string) error {
	return fmt.Errorf("%w: keychain backend is only available on macOS", errKeychainUnavailable)
}
