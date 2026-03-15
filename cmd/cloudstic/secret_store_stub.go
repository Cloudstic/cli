//go:build !darwin

package main

import (
	"context"
	"errors"
)

func saveSecretToNativeStore(_ context.Context, _, _, _ string) error {
	return errors.New("native secret write not supported on this platform")
}

func nativeSecretExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
