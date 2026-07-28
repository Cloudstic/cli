//go:build !linux

package backends

import (
	"context"
	"fmt"
)

func defaultSecretServiceWriteSupported() bool { return false }

func defaultSecretServiceLookup(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("%w: secret service backend is only available on Linux; use env://... as a fallback in headless environments", errSecretServiceUnavailable)
}

func defaultSecretServiceExists(_ context.Context, _, _ string) (bool, error) {
	return false, fmt.Errorf("%w: secret service backend is only available on Linux; use env://... as a fallback in headless environments", errSecretServiceUnavailable)
}

func defaultSecretServiceStore(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("%w: secret service backend is only available on Linux; use env://... as a fallback in headless environments", errSecretServiceUnavailable)
}
