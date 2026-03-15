package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudstic/cli/internal/secretref"
)

var profileSecretResolver = secretref.NewDefaultResolver()

func resolveProfileStoreValue(fieldName, direct, secretRef, envName string) (string, error) {
	if direct != "" {
		return direct, nil
	}
	if secretRef != "" {
		v, err := profileSecretResolver.Resolve(context.Background(), secretRef)
		if err != nil {
			return "", fmt.Errorf("resolve profile store field %q from %q: %w", fieldName, secretRef, err)
		}
		return v, nil
	}
	if envName != "" {
		return os.Getenv(envName), nil
	}
	return "", nil
}
