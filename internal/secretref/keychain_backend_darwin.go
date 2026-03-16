//go:build darwin

package secretref

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func defaultKeychainLookup(ctx context.Context, service, account string) (string, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%w: macOS security tool not found", errKeychainUnavailable)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(err.Error())
		}
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "could not be found") || strings.Contains(lower, "item not found") {
			return "", errKeychainNotFound
		}
		if strings.Contains(lower, "user interaction is not allowed") || strings.Contains(lower, "interaction not allowed") {
			return "", fmt.Errorf("%w: keychain locked or unavailable in this session", errKeychainUnavailable)
		}
		return "", fmt.Errorf("security find-generic-password failed: %s", msg)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}
