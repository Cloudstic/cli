package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
)

func newTestGlobalFlags(args ...string) *globalFlags {
	g := &globalFlags{}
	_ = newCommandFlags("test", repoCommandGroups, g, commandInput{}).parse(args)
	return g
}

func testProvided(g *globalFlags, names ...string) *globalFlags {
	if g.origins == nil {
		g.origins = map[string]flagOrigin{}
	}
	for _, name := range names {
		g.origins[name] = originFlag
	}
	return g
}

func TestEnsureDefaultAuthRef_CreatesDefaultEntry(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	bcfg, err := ensureDefaultAuthRef(
		config.Backup{Source: config.Source{URI: "gdrive-changes:/Docs"}}, profilesPath)
	if err != nil {
		t.Fatalf("ensureDefaultAuthRef: %v", err)
	}
	if bcfg.AuthRef != "google-default" {
		t.Fatalf("AuthRef=%q want google-default", bcfg.AuthRef)
	}
	// The token path must be folded back into the configuration, or the source
	// would authenticate against a different file than the entry records.
	if bcfg.Source.Google.TokenPath == "" {
		t.Fatal("expected the Google token path to be set")
	}
	if _, err := os.Stat(profilesPath); err != nil {
		t.Fatalf("expected profiles file to exist: %v", err)
	}
	cfg, err := profile.Load(profilesPath)
	if err != nil {
		t.Fatalf("profile.Load: %v", err)
	}
	auth, ok := cfg.Auth["google-default"]
	if !ok {
		t.Fatal("missing google-default auth entry")
	}
	if auth.Provider != "google" {
		t.Fatalf("provider=%q want google", auth.Provider)
	}
}

func TestEnsureDefaultAuthRef_NonCloudNoop(t *testing.T) {
	bcfg, err := ensureDefaultAuthRef(
		config.Backup{Source: config.Source{URI: "local:/tmp"}},
		filepath.Join(t.TempDir(), "profiles.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bcfg.AuthRef != "" {
		t.Fatalf("AuthRef=%q want empty for a non-cloud source", bcfg.AuthRef)
	}
}

func applyTestProfileStore(t *testing.T, s profile.Store, provided ...string) (clientConfig, error) {
	t.Helper()
	cfg := clientConfigFromFlags(newTestGlobalFlags())
	set := map[string]bool{}
	for _, name := range provided {
		set[name] = true
	}
	err := applyProfileStore(&cfg, s, "", func(name string) bool { return set[name] })
	return cfg, err
}

func TestApplyProfileStore_AllFields(t *testing.T) {
	t.Setenv("TEST_PASSWORD", "secret-pw")
	t.Setenv("TEST_ENC_KEY", "enc-key-val")
	t.Setenv("TEST_REC_KEY", "rec-key-val")

	cfg, err := applyTestProfileStore(t, profile.Store{
		URI:                 "s3:my-bucket/prefix",
		S3Region:            "us-east-1",
		S3Endpoint:          "https://s3.example.com",
		S3Profile:           "prod",
		S3AccessKey:         "AKIATEST",
		S3SecretKey:         "SECRETTEST",
		B2KeyID:             "b2-key-id",
		B2AppKey:            "b2-app-key",
		StoreSFTPPassword:   "sftp-pw",
		StoreSFTPKey:        "/tmp/sftp.key",
		PasswordSecret:      "env://TEST_PASSWORD",
		EncryptionKeySecret: "env://TEST_ENC_KEY",
		RecoveryKeySecret:   "env://TEST_REC_KEY",
		KMSKeyARN:           "arn:aws:kms:us-east-1:123:key/abc",
		KMSRegion:           "us-east-1",
		KMSEndpoint:         "https://kms.example.com",
	})
	if err != nil {
		t.Fatalf("applyProfileStore: %v", err)
	}

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"store.uri", cfg.Store.URI, "s3:my-bucket/prefix"},
		{"s3.region", cfg.Store.S3.Region, "us-east-1"},
		{"s3.endpoint", cfg.Store.S3.Endpoint, "https://s3.example.com"},
		{"s3.profile", cfg.Store.S3.Profile, "prod"},
		{"s3.accessKey", cfg.Store.S3.AccessKey, "AKIATEST"},
		{"s3.secretKey", cfg.Store.S3.SecretKey, "SECRETTEST"},
		{"b2.keyID", cfg.Store.B2.KeyID, "b2-key-id"},
		{"b2.appKey", cfg.Store.B2.AppKey, "b2-app-key"},
		{"sftp.password", cfg.Store.SFTP.Password, "sftp-pw"},
		{"sftp.key", cfg.Store.SFTP.Key, "/tmp/sftp.key"},
		{"unlock.password", cfg.Unlock.Password, "secret-pw"},
		{"unlock.encryptionKey", cfg.Unlock.EncryptionKey, "enc-key-val"},
		{"unlock.recoveryKey", cfg.Unlock.RecoveryKey, "rec-key-val"},
		{"kms.keyARN", cfg.Unlock.KMS.KeyARN, "arn:aws:kms:us-east-1:123:key/abc"},
		{"kms.region", cfg.Unlock.KMS.Region, "us-east-1"},
		{"kms.endpoint", cfg.Unlock.KMS.Endpoint, "https://kms.example.com"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s=%q want %q", c.name, c.got, c.want)
		}
	}
}

func TestApplyProfileStore_CLIFlagOverrides(t *testing.T) {
	cfg := clientConfigFromFlags(func() *globalFlags {
		g := testProvided(newTestGlobalFlags(), "store")
		g.store = "local:/cli-store"
		return g
	}())
	err := applyProfileStore(&cfg, profile.Store{URI: "s3:profile-bucket"}, "",
		func(name string) bool { return name == "store" })
	if err != nil {
		t.Fatalf("applyProfileStore: %v", err)
	}
	if cfg.Store.URI != "local:/cli-store" {
		t.Fatalf("store.uri=%q want local:/cli-store", cfg.Store.URI)
	}
}

func TestApplyProfileStore_SecretRef(t *testing.T) {
	t.Setenv("SECRET_PW", "from-secret-ref")

	cfg, err := applyTestProfileStore(t, profile.Store{PasswordSecret: "env://SECRET_PW"})
	if err != nil {
		t.Fatalf("applyProfileStore: %v", err)
	}
	if cfg.Unlock.Password != "from-secret-ref" {
		t.Fatalf("unlock.password=%q want from-secret-ref", cfg.Unlock.Password)
	}
}

func TestApplyProfileStore_B2SecretRef(t *testing.T) {
	t.Setenv("SECRET_B2_KEY_ID", "b2-id-from-env")
	t.Setenv("SECRET_B2_APP_KEY", "b2-key-from-env")

	cfg, err := applyTestProfileStore(t, profile.Store{
		B2KeyIDSecret:  "env://SECRET_B2_KEY_ID",
		B2AppKeySecret: "env://SECRET_B2_APP_KEY",
	})
	if err != nil {
		t.Fatalf("applyProfileStore: %v", err)
	}
	if cfg.Store.B2.KeyID != "b2-id-from-env" {
		t.Fatalf("b2.keyID=%q want b2-id-from-env", cfg.Store.B2.KeyID)
	}
	if cfg.Store.B2.AppKey != "b2-key-from-env" {
		t.Fatalf("b2.appKey=%q want b2-key-from-env", cfg.Store.B2.AppKey)
	}
}

func TestApplyProfileStore_InvalidSecretRef(t *testing.T) {
	_, err := applyTestProfileStore(t, profile.Store{PasswordSecret: "env:/invalid-format"})
	if err == nil {
		t.Fatal("expected error for invalid secret ref")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected field context in error, got: %v", err)
	}
}

func TestEnsureDefaultAuthRef_OneDrive(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	bcfg, err := ensureDefaultAuthRef(
		config.Backup{Source: config.Source{URI: "onedrive:/Documents"}}, profilesPath)
	if err != nil {
		t.Fatalf("ensureDefaultAuthRef: %v", err)
	}
	if bcfg.AuthRef != "onedrive-default" {
		t.Fatalf("AuthRef=%q want onedrive-default", bcfg.AuthRef)
	}
	if bcfg.Source.OneDrive.TokenPath == "" {
		t.Fatal("expected the OneDrive token path to be set")
	}
}
