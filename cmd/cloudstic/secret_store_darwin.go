//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

var execCommandContext = exec.CommandContext

func saveSecretToNativeStore(ctx context.Context, service, account, value string) error {
	cmd := execCommandContext(ctx, "security", "add-generic-password", "-U", "-s", service, "-a", account, "-w")
	cmd.Stdin = strings.NewReader(value + "\n" + value + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("save secret in macOS keychain failed: %s", msg)
	}
	return nil
}

func nativeSecretExists(ctx context.Context, service, account string) (bool, error) {
	cmd := execCommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", account)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return false, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return false, fmt.Errorf("check secret in macOS keychain failed: %s", msg)
	}
	return true, nil
}
