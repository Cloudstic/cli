package main

import (
	"context"
	"fmt"

	secretrefbackends "github.com/cloudstic/cli/pkg/secretref/backends"
)

var profileSecretResolver = secretrefbackends.NewDefaultResolver()

func resolveProfileStoreValue(fieldName, direct, secretRef string) (string, error) {
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
	return "", nil
}
