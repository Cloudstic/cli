package main

import (
	"fmt"
	"github.com/cloudstic/cli/pkg/config"

	"github.com/cloudstic/cli/pkg/profile"

	"github.com/cloudstic/cli/internal/tui/forms"
	"github.com/cloudstic/cli/pkg/secretref"
)

// Store form backend (tui/forms.StoreBackend), implemented by reusing the
// existing store-URI and secret-ref helpers so the forms package stays free of
// parsing and persistence concerns.

func (b *tuiFormsBackend) StoreDetailLabel(storeType string) string {
	return tuiStoreConfig{Type: storeType}.DetailLabel()
}

func (b *tuiFormsBackend) StoreExample(storeType string) string {
	return tuiStoreConfig{Type: storeType}.ExampleText()
}

func (b *tuiFormsBackend) ComposeStore(storeType, value string) (string, error) {
	uri := tuiStoreConfig{Type: storeType, Value: value}.Compose()
	if uri == "" {
		return "", nil
	}
	if _, err := config.ParseStoreURI(uri); err != nil {
		return "", fmt.Errorf("invalid store: %w", err)
	}
	return uri, nil
}

func (b *tuiFormsBackend) ValidateStoreName(name string) error {
	return validateRefName("store", name)
}

func (b *tuiFormsBackend) StoreExists(name string) bool {
	if b.cfg == nil {
		return false
	}
	_, ok := b.cfg.Stores[name]
	return ok
}

func (b *tuiFormsBackend) ValidateSecretRef(ref string) error {
	if _, err := secretref.Parse(ref); err != nil {
		return fmt.Errorf("invalid secret reference: %w", err)
	}
	return nil
}

// InitialStoreValues seeds an edit-store form from an existing store.
func (b *tuiFormsBackend) InitialStoreValues(name string) map[string]string {
	if b.cfg == nil {
		return nil
	}
	store, ok := b.cfg.Stores[name]
	if !ok {
		return nil
	}
	cfg := newTUIStoreConfig(store.URI)
	return map[string]string{
		forms.FieldStoreType:      cfg.Type,
		forms.FieldStoreValue:     cfg.Value,
		forms.FieldS3Region:       store.S3Region,
		forms.FieldS3Profile:      store.S3Profile,
		forms.FieldS3Endpoint:     store.S3Endpoint,
		forms.FieldS3AccessKey:    store.S3AccessKeySecret,
		forms.FieldS3SecretKey:    store.S3SecretKeySecret,
		forms.FieldSFTPPassword:   store.StoreSFTPPasswordSecret,
		forms.FieldSFTPKey:        store.StoreSFTPKeySecret,
		forms.FieldEncryptionMode: string(newTUIStoreEncryptionMode(store)),
		forms.FieldPasswordSecret: store.PasswordSecret,
		forms.FieldPlatformSecret: store.EncryptionKeySecret,
		forms.FieldKMSKeyARN:      store.KMSKeyARN,
		forms.FieldKMSRegion:      store.KMSRegion,
		forms.FieldKMSEndpoint:    store.KMSEndpoint,
	}
}

// SaveStore assembles a ProfileStore from the form's collected values and
// persists it. Connection and encryption fields irrelevant to the selected
// type/mode are cleared, mirroring the legacy store form.
func (b *tuiFormsBackend) SaveStore(name string, values map[string]string, editing bool) error {
	store := profile.Store{}
	if editing && b.cfg != nil {
		if existing, ok := b.cfg.Stores[name]; ok {
			store = existing
		}
	}
	store.URI = values[forms.FieldStoreURI]
	applyStoreConnectionValues(&store, values)
	applyStoreEncryptionValues(&store, values)

	if err := tuiServiceFactory(nil, b.profilesFile, b.configDir).SaveStore(b.profilesFile, name, store); err != nil {
		return err
	}
	// Refresh the cache so the profile form's store selector immediately sees
	// the new/edited store.
	if cfg, err := tuiLoadConfig(b.profilesFile); err == nil {
		b.cfg = cfg
	}
	return nil
}

func applyStoreConnectionValues(store *profile.Store, values map[string]string) {
	// Start clean; only the selected type's fields are populated.
	store.S3Region = ""
	store.S3Profile = ""
	store.S3Endpoint = ""
	store.S3AccessKeySecret = ""
	store.S3SecretKeySecret = ""
	store.StoreSFTPPasswordSecret = ""
	store.StoreSFTPKeySecret = ""

	switch values[forms.FieldStoreType] {
	case "s3", "b2":
		store.S3Region = values[forms.FieldS3Region]
		store.S3Profile = values[forms.FieldS3Profile]
		store.S3Endpoint = values[forms.FieldS3Endpoint]
		store.S3AccessKeySecret = values[forms.FieldS3AccessKey]
		store.S3SecretKeySecret = values[forms.FieldS3SecretKey]
	case "sftp":
		store.StoreSFTPPasswordSecret = values[forms.FieldSFTPPassword]
		store.StoreSFTPKeySecret = values[forms.FieldSFTPKey]
	}
}

func applyStoreEncryptionValues(store *profile.Store, values map[string]string) {
	store.PasswordSecret = ""
	store.EncryptionKeySecret = ""
	store.KMSKeyARN = ""
	store.KMSRegion = ""
	store.KMSEndpoint = ""

	switch values[forms.FieldEncryptionMode] {
	case forms.EncPassword:
		store.PasswordSecret = values[forms.FieldPasswordSecret]
	case forms.EncPlatform:
		store.EncryptionKeySecret = values[forms.FieldPlatformSecret]
	case forms.EncKMS:
		store.KMSKeyARN = values[forms.FieldKMSKeyARN]
		store.KMSRegion = firstNonEmpty(values[forms.FieldKMSRegion], "us-east-1")
		store.KMSEndpoint = values[forms.FieldKMSEndpoint]
	}
}
