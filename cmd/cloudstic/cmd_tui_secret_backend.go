package main

import (
	"context"
	"strings"

	"github.com/cloudstic/cli/pkg/secretref"
	secretrefbackends "github.com/cloudstic/cli/pkg/secretref/backends"
)

// Secret-ref form backend (tui/forms.SecretBackend), implemented over the
// shared secretref resolver.

func (b *tuiFormsBackend) secretResolver() *secretref.Resolver {
	if tuiSecretResolver != nil {
		return tuiSecretResolver
	}
	return secretrefbackends.NewDefaultResolver()
}

func (b *tuiFormsBackend) StorageOptions() []string {
	options := []string{"env"}
	for _, backend := range b.secretResolver().WritableBackends() {
		options = append(options, backend.Scheme())
	}
	return options
}

func (b *tuiFormsBackend) DefaultRef(scheme, storeName, account string) string {
	for _, backend := range b.secretResolver().WritableBackends() {
		if backend.Scheme() == scheme {
			return backend.DefaultRef(storeName, account)
		}
	}
	return ""
}

func (b *tuiFormsBackend) ParseRef(ref string) (string, string, bool) {
	parsed, err := secretref.Parse(ref)
	if err != nil {
		return "", "", false
	}
	if parsed.Scheme == "env" {
		return "env", strings.TrimLeft(parsed.Path, "/"), true
	}
	return parsed.Scheme, "", true
}

func (b *tuiFormsBackend) ValidateRef(ref string) error {
	_, err := secretref.Parse(ref)
	return err
}

func (b *tuiFormsBackend) StoreSecret(ref, value string) error {
	return b.secretResolver().Store(context.Background(), ref, value)
}
