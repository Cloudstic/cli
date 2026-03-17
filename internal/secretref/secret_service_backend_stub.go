//go:build !linux

package secretref

import (
	"context"
	"fmt"
)

func defaultSecretServiceLookup(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("%w: secret service backend is only available on Linux; use env://... as a fallback in headless environments", errSecretServiceUnavailable)
}
