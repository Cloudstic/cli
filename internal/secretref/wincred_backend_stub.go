//go:build !windows

package secretref

import (
	"context"
	"fmt"
)

func defaultWincredWriteSupported() bool { return false }

func defaultWincredLookup(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("%w: windows credential backend is only available on Windows", errWincredUnavailable)
}

func defaultWincredExists(_ context.Context, _ string) (bool, error) {
	return false, fmt.Errorf("%w: windows credential backend is only available on Windows", errWincredUnavailable)
}

func defaultWincredStore(_ context.Context, _, _ string) error {
	return fmt.Errorf("%w: windows credential backend is only available on Windows", errWincredUnavailable)
}
