package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

// TestResolveClientConfig_ProfileOverridesEnvironment documents and locks in
// the precedence #266 asked to make explicit: a selected profile is a named
// choice the user invoked with -profile, so it overrides an ambient
// environment variable the same way it overrides a built-in default. Only an
// explicit CLI flag (TestApplyProfileStore_CLIFlagOverrides) beats it.
func TestResolveClientConfig_ProfileOverridesEnvironment(t *testing.T) {
	t.Setenv("CLOUDSTIC_S3_REGION", "env-region")
	g := newTestGlobalFlags()
	if g.s3Region != "env-region" {
		t.Fatalf("s3Region=%q want env-region (environment should win over default)", g.s3Region)
	}
	if g.origins["s3-region"] != originEnv {
		t.Fatalf("origins[s3-region]=%v want originEnv", g.origins["s3-region"])
	}

	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"s": {URI: "s3:bucket", S3Region: "profile-region"},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"p": {Source: "local:/data", Store: "s"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}
	g.profile = "p"
	g.profilesFile = profilesPath

	resolved, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if resolved.store.s3.region != "profile-region" {
		t.Fatalf("store.s3.region=%q want profile-region (profile should win over environment)", resolved.store.s3.region)
	}
}

func TestResolveClientConfig_AppliesProfileStore(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"s": {URI: "s3:bucket/prefix", S3Region: "eu-west-1", S3Profile: "prod"},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"p": {Source: "local:/data", Store: "s"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	g := newTestGlobalFlags()
	g.profile = "p"
	g.profilesFile = profilesPath

	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if cfg.store.uri != "s3:bucket/prefix" {
		t.Fatalf("store=%q want s3:bucket/prefix", cfg.store.uri)
	}
	if cfg.store.s3.region != "eu-west-1" {
		t.Fatalf("s3Region=%q want eu-west-1", cfg.store.s3.region)
	}
	if cfg.store.s3.profile != "prod" {
		t.Fatalf("s3Profile=%q want prod", cfg.store.s3.profile)
	}
}

func TestResolveClientConfig_EncryptionFields(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	t.Setenv("TEST_BACKUP_PASSWORD", "s3cret")
	t.Setenv("TEST_ENC_KEY", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("TEST_RECOVERY_KEY", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")

	err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"enc": {
				URI:                 "s3:bucket/enc",
				PasswordSecret:      "env://TEST_BACKUP_PASSWORD",
				EncryptionKeySecret: "env://TEST_ENC_KEY",
				RecoveryKeySecret:   "env://TEST_RECOVERY_KEY",
				KMSKeyARN:           "arn:aws:kms:us-east-1:123456:key/abcd",
				KMSRegion:           "us-east-1",
				KMSEndpoint:         "https://kms.example.com",
			},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"p": {Source: "local:/data", Store: "enc"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	g := newTestGlobalFlags()
	g.profile = "p"
	g.profilesFile = profilesPath

	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if cfg.unlock.password != "s3cret" {
		t.Fatalf("password=%q want s3cret", cfg.unlock.password)
	}
	if cfg.unlock.encryptionKey != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("encryptionKey=%q want aaa...", cfg.unlock.encryptionKey)
	}
	if cfg.unlock.recoveryKey == "" {
		t.Fatal("expected recoveryKey to be set from env")
	}
	if cfg.unlock.kms.keyARN != "arn:aws:kms:us-east-1:123456:key/abcd" {
		t.Fatalf("kmsKeyARN=%q want arn:...", cfg.unlock.kms.keyARN)
	}
	if cfg.unlock.kms.region != "us-east-1" {
		t.Fatalf("kmsRegion=%q want us-east-1", cfg.unlock.kms.region)
	}
	if cfg.unlock.kms.endpoint != "https://kms.example.com" {
		t.Fatalf("kmsEndpoint=%q want https://kms.example.com", cfg.unlock.kms.endpoint)
	}
}

func TestResolveClientConfig_DoesNotOverrideExplicitStoreFlag(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"s": {URI: "s3:bucket/prefix"},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"p": {Source: "local:/data", Store: "s"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	g := newTestGlobalFlags("-store", "local:/explicit")
	g.profile = "p"
	g.profilesFile = profilesPath
	g.store = "local:/explicit"

	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if cfg.store.uri != "local:/explicit" {
		t.Fatalf("store=%q want local:/explicit", cfg.store.uri)
	}
	// Resolution must not write back into the parsed flags.
	if g.store != "local:/explicit" {
		t.Fatalf("parsed flags mutated: store=%q", g.store)
	}
}

// globalFlags holds parsed command-line input. It is deliberately not a
// factory: store, client, keychain, and KMS construction live in
// storebuild.go/keychain.go and take resolved configuration values, so they can
// be tested without flag parsing. Only accessors over the parsed values belong
// here.
func TestGlobalFlagsHasNoConstructionMethods(t *testing.T) {
	allowed := map[string]bool{
		"jsonEnabled":  true,
		"flagProvided": true,
	}

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	fset := token.NewFileSet()
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := star.X.(*ast.Ident)
			if !ok || ident.Name != "globalFlags" {
				continue
			}
			if !allowed[fn.Name.Name] {
				t.Errorf("%s: globalFlags must not have method %q; make it a function taking a resolved config value",
					path, fn.Name.Name)
			}
		}
	}
}
