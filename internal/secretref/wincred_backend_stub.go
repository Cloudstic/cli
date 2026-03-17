//go:build !windows

package secretref

import (
	"context"
	"fmt"
)

func defaultWincredLookup(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("%w: windows credential backend is only available on Windows", errWincredUnavailable)
}
