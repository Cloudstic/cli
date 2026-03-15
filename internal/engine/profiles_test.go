package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProfilesFile_NormalizesMapsAndVersion(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "profiles.yaml")
	if err := os.WriteFile(path, []byte("profiles:\n  a:\n    source: local:/tmp\n"), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cfg, err := LoadProfilesFile(path)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("Version=%d want=1", cfg.Version)
	}
	if cfg.Stores == nil {
		t.Fatal("Stores map should be initialized")
	}
	if cfg.Auth == nil {
		t.Fatal("Auth map should be initialized")
	}
	if cfg.Profiles == nil || len(cfg.Profiles) != 1 {
		t.Fatalf("Profiles map invalid: %#v", cfg.Profiles)
	}
}

func TestSaveProfilesFile_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "profiles.yaml")

	err := SaveProfilesFile(path, &ProfilesConfig{
		Stores: map[string]ProfileStore{
			"s": {
				URI:                     "local:./store",
				PasswordSecret:          "env://CLOUDSTIC_PASSWORD",
				EncryptionKeySecret:     "env://CLOUDSTIC_ENCRYPTION_KEY",
				RecoveryKeySecret:       "keychain://cloudstic/recovery",
				S3AccessKeySecret:       "env://AWS_ACCESS_KEY_ID",
				S3SecretKeySecret:       "env://AWS_SECRET_ACCESS_KEY",
				StoreSFTPPasswordSecret: "env://STORE_SFTP_PASSWORD",
				StoreSFTPKeySecret:      "env://STORE_SFTP_KEY",
			},
		},
		Profiles: map[string]BackupProfile{
			"p": {Source: "local:./docs", Store: "s"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	cfg, err := LoadProfilesFile(path)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	if cfg.Profiles["p"].Source != "local:./docs" {
		t.Fatalf("unexpected source: %q", cfg.Profiles["p"].Source)
	}
	if cfg.Stores["s"].URI != "local:./store" {
		t.Fatalf("unexpected store uri: %q", cfg.Stores["s"].URI)
	}
	if cfg.Stores["s"].PasswordSecret != "env://CLOUDSTIC_PASSWORD" {
		t.Fatalf("unexpected password secret: %q", cfg.Stores["s"].PasswordSecret)
	}
	if cfg.Stores["s"].S3SecretKeySecret != "env://AWS_SECRET_ACCESS_KEY" {
		t.Fatalf("unexpected s3 secret ref: %q", cfg.Stores["s"].S3SecretKeySecret)
	}
}

func TestLoadProfilesFile_InvalidSecretRef(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "profiles.yaml")
	content := `version: 1
stores:
  prod:
    uri: local:./store
    password_secret: invalid
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := LoadProfilesFile(path)
	if err == nil {
		t.Fatal("LoadProfilesFile: expected validation error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `store "prod" field "password_secret"`) {
		t.Fatalf("expected actionable field context in error, got: %v", err)
	}
	if !strings.Contains(msg, "<scheme>://<path>") {
		t.Fatalf("expected actionable format hint in error, got: %v", err)
	}
}

func TestSaveProfilesFile_InvalidSecretRef(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "profiles.yaml")
	err := SaveProfilesFile(path, &ProfilesConfig{
		Stores: map[string]ProfileStore{
			"prod": {
				URI:                "local:./store",
				StoreSFTPKeySecret: "env:/bad-format",
			},
		},
	})
	if err == nil {
		t.Fatal("SaveProfilesFile: expected validation error")
	}
	if !strings.Contains(err.Error(), `store "prod" field "store_sftp_key_secret"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
