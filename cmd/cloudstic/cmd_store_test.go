package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/keychain"
)

func TestRunStoreNewAndListAndShow(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	// Create a store.
	os.Args = []string{
		"cloudstic", "store", "new",
		"-profiles-file", profilesPath,
		"-name", "prod-s3",
		"-uri", "s3:my-bucket/backups",
		"-s3-region", "eu-west-1",
		"-s3-profile", "prod",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store new failed: %s", errOut.String())
	}
	if !strings.Contains(out.String(), `"prod-s3" saved`) {
		t.Fatalf("unexpected store new output: %s", out.String())
	}

	// List stores.
	os.Args = []string{"cloudstic", "store", "list", "-profiles-file", profilesPath}
	out.Reset()
	errOut.Reset()
	if code := r.runStore(); code != 0 {
		t.Fatalf("store list failed: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "Stores") || !strings.Contains(out.String(), "prod-s3") || !strings.Contains(out.String(), "AUTH") {
		t.Fatalf("unexpected store list output:\n%s", out.String())
	}

	// Show store.
	os.Args = []string{"cloudstic", "store", "show", "-profiles-file", profilesPath, "prod-s3"}
	out.Reset()
	errOut.Reset()
	if code := r.runStore(); code != 0 {
		t.Fatalf("store show failed: %s", errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Store prod-s3") {
		t.Fatalf("expected store name in show output:\n%s", got)
	}
	if !strings.Contains(got, "s3:my-bucket/backups") {
		t.Fatalf("expected URI in show output:\n%s", got)
	}
	if !strings.Contains(got, "Connection") || !strings.Contains(got, "eu-west-1") {
		t.Fatalf("expected region in show output:\n%s", got)
	}
	if !strings.Contains(got, "prod") || !strings.Contains(got, "Used By") {
		t.Fatalf("expected profile in show output:\n%s", got)
	}
}

func TestRunStoreNew_RequiresNameAndURI(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	// Missing URI.
	os.Args = []string{"cloudstic", "store", "new", "-profiles-file", profilesPath, "-name", "x"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "-uri is required") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
}

func TestRunStoreNew_ExistingStorePrefillsUnsetValues(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	cfg := &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"prod": {
				URI:            "s3:bucket/backups",
				S3Region:       "us-east-1",
				S3Profile:      "old-profile",
				PasswordSecret: "env://OLD_PASSWORD",
			},
		},
	}
	if err := cloudstic.SaveProfilesFile(profilesPath, cfg); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	os.Args = []string{
		"cloudstic", "store", "new",
		"-profiles-file", profilesPath,
		"-name", "prod",
		"-s3-profile", "new-profile",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store new failed: %s", errOut.String())
	}

	updated, err := cloudstic.LoadProfilesFile(profilesPath)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	s := updated.Stores["prod"]
	if s.URI != "s3:bucket/backups" {
		t.Fatalf("uri=%q", s.URI)
	}
	if s.S3Region != "us-east-1" {
		t.Fatalf("s3 region=%q", s.S3Region)
	}
	if s.S3Profile != "new-profile" {
		t.Fatalf("s3 profile=%q", s.S3Profile)
	}
	if s.PasswordSecret != "env://OLD_PASSWORD" {
		t.Fatalf("password secret=%q", s.PasswordSecret)
	}
}

func TestNoPromptDisablesInteractivity(t *testing.T) {
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, noPrompt: true}
	if r.canPrompt() {
		t.Fatal("canPrompt() should return false when noPrompt is true")
	}
}

func TestHasGlobalFlag(t *testing.T) {
	orig := os.Args
	defer func() { os.Args = orig }()

	os.Args = []string{"cloudstic", "store", "new", "--no-prompt", "-name", "x"}
	if !hasGlobalFlag("no-prompt") {
		t.Fatal("expected hasGlobalFlag to find --no-prompt")
	}

	os.Args = []string{"cloudstic", "store", "new", "-name", "x"}
	if hasGlobalFlag("no-prompt") {
		t.Fatal("expected hasGlobalFlag to not find --no-prompt")
	}
}

func TestRunStoreShow_UnknownStore(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	if err := os.WriteFile(profilesPath, []byte("version: 1\nstores: {}\n"), 0600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}

	os.Args = []string{"cloudstic", "store", "show", "-profiles-file", profilesPath, "missing"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "Unknown store") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
}

func TestRunStoreShow_UsedBy(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  main:
    uri: s3:bucket/path
profiles:
  photos:
    source: local:/photos
    store: main
  docs:
    source: local:/docs
    store: main
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}

	os.Args = []string{"cloudstic", "store", "show", "-profiles-file", profilesPath, "main"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store show failed: %s", errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Used By") || !strings.Contains(got, "docs") || !strings.Contains(got, "photos") {
		t.Fatalf("expected used_by with both profiles:\n%s", got)
	}
}

func TestRunStoreList_MissingFile(t *testing.T) {
	os.Args = []string{"cloudstic", "store", "list", "-profiles-file", filepath.Join(t.TempDir(), "missing.yaml")}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("expected zero exit code, got err=%s", errOut.String())
	}
}

func TestRunStoreNew_WithEncryption(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{
		"cloudstic", "store", "new",
		"-profiles-file", profilesPath,
		"-name", "encrypted-s3",
		"-uri", "s3:secure-bucket/backups",
		"-password-env", "MY_BACKUP_PASSWORD",
		"-kms-key-arn", "arn:aws:kms:us-east-1:123456:key/abcd",
		"-kms-region", "us-east-1",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store new failed: %s", errOut.String())
	}

	// Verify show displays encryption fields.
	os.Args = []string{"cloudstic", "store", "show", "-profiles-file", profilesPath, "encrypted-s3"}
	out.Reset()
	errOut.Reset()
	if code := r.runStore(); code != 0 {
		t.Fatalf("store show failed: %s", errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"Password Secret",
		"env://MY_BACKUP_PASSWORD",
		"KMS Key ARN",
		"arn:aws:kms:us-east-1:123456:key/abcd",
		"KMS Region",
		"us-east-1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in show output:\n%s", want, got)
		}
	}

	// Verify YAML persistence.
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles: %v", err)
	}
	yaml := string(raw)
	if !strings.Contains(yaml, "password_secret: env://MY_BACKUP_PASSWORD") {
		t.Fatalf("expected password_secret in YAML:\n%s", yaml)
	}
	if !strings.Contains(yaml, "kms_key_arn:") {
		t.Fatalf("expected kms_key_arn in YAML:\n%s", yaml)
	}
}

func TestCheckOrInitStore_AlreadyInitialized(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")

	// Initialize the store first.
	s := cloudstic.ProfileStore{URI: "local:" + storePath}
	g, err := globalFlagsFromProfileStore(s)
	if err != nil {
		t.Fatalf("globalFlagsFromProfileStore: %v", err)
	}
	raw, err := g.initObjectStore()
	if err != nil {
		t.Fatalf("initObjectStore: %v", err)
	}
	_, err = cloudstic.InitRepo(t.Context(), raw, cloudstic.WithInitNoEncryption())
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	cfg := &cloudstic.ProfilesConfig{
		Version: 1,
		Stores:  map[string]cloudstic.ProfileStore{"test": s},
	}
	if err := cloudstic.SaveProfilesFile(profilesPath, cfg); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if err := r.checkOrInitStore(cfg, "test", profilesPath, checkOrInitOptions{warnOnMissingSecrets: true, offerInit: true}); err != nil {
		t.Fatalf("checkOrInitStore: %v", err)
	}

	if !strings.Contains(out.String(), "already initialized") {
		t.Fatalf("expected 'already initialized' in output, got:\n%s", out.String())
	}
}

func TestCheckOrInitStore_InitializedEncrypted_ValidCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")

	s := cloudstic.ProfileStore{URI: "local:" + storePath}
	g, err := globalFlagsFromProfileStore(s)
	if err != nil {
		t.Fatalf("globalFlagsFromProfileStore: %v", err)
	}
	raw, err := g.initObjectStore()
	if err != nil {
		t.Fatalf("initObjectStore: %v", err)
	}
	_, err = cloudstic.InitRepo(t.Context(), raw, cloudstic.WithInitCredentials(keychain.Chain{keychain.WithPassword("correct-password")}))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	t.Setenv("VERIFY_STORE_PASSWORD", "correct-password")
	cfg := &cloudstic.ProfilesConfig{Version: 1, Stores: map[string]cloudstic.ProfileStore{
		"test": {
			URI:            s.URI,
			PasswordSecret: "env://VERIFY_STORE_PASSWORD",
		},
	}}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if err := r.checkOrInitStore(cfg, "test", "profiles.yaml", checkOrInitOptions{warnOnMissingSecrets: true, offerInit: true}); err != nil {
		t.Fatalf("checkOrInitStore: %v", err)
	}
	if !strings.Contains(out.String(), "Repository is encrypted; verifying configured credentials") {
		t.Fatalf("missing verification message in output: %s", out.String())
	}
	if !strings.Contains(out.String(), "Encryption credentials are valid") {
		t.Fatalf("missing success message in output: %s", out.String())
	}
}

func TestCheckOrInitStore_InitializedEncrypted_InvalidCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")

	s := cloudstic.ProfileStore{URI: "local:" + storePath}
	g, err := globalFlagsFromProfileStore(s)
	if err != nil {
		t.Fatalf("globalFlagsFromProfileStore: %v", err)
	}
	raw, err := g.initObjectStore()
	if err != nil {
		t.Fatalf("initObjectStore: %v", err)
	}
	_, err = cloudstic.InitRepo(t.Context(), raw, cloudstic.WithInitCredentials(keychain.Chain{keychain.WithPassword("correct-password")}))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	t.Setenv("VERIFY_STORE_PASSWORD_BAD", "wrong-password")
	cfg := &cloudstic.ProfilesConfig{Version: 1, Stores: map[string]cloudstic.ProfileStore{
		"test": {
			URI:            s.URI,
			PasswordSecret: "env://VERIFY_STORE_PASSWORD_BAD",
		},
	}}

	r := &runner{out: &strings.Builder{}, errOut: &strings.Builder{}}
	err = r.checkOrInitStore(cfg, "test", "profiles.yaml", checkOrInitOptions{warnOnMissingSecrets: true, offerInit: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "configured encryption credentials are invalid") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGlobalFlagsFromProfileStore_ResolvesEnvVars(t *testing.T) {
	t.Setenv("TEST_AK", "my-access-key")
	t.Setenv("TEST_SK", "my-secret-key")
	t.Setenv("TEST_PW", "s3cret")

	s := cloudstic.ProfileStore{
		URI:            "s3:bucket/prefix",
		S3Region:       "eu-west-1",
		S3AccessKeyEnv: "TEST_AK",
		S3SecretKeyEnv: "TEST_SK",
		PasswordEnv:    "TEST_PW",
		KMSKeyARN:      "arn:aws:kms:us-east-1:123:key/abc",
	}

	g, err := globalFlagsFromProfileStore(s)
	if err != nil {
		t.Fatalf("globalFlagsFromProfileStore: %v", err)
	}
	if *g.store != "s3:bucket/prefix" {
		t.Fatalf("store=%q", *g.store)
	}
	if *g.s3Region != "eu-west-1" {
		t.Fatalf("s3Region=%q", *g.s3Region)
	}
	if *g.s3AccessKey != "my-access-key" {
		t.Fatalf("s3AccessKey=%q", *g.s3AccessKey)
	}
	if *g.s3SecretKey != "my-secret-key" {
		t.Fatalf("s3SecretKey=%q", *g.s3SecretKey)
	}
	if *g.password != "s3cret" {
		t.Fatalf("password=%q", *g.password)
	}
	if *g.kmsKeyARN != "arn:aws:kms:us-east-1:123:key/abc" {
		t.Fatalf("kmsKeyARN=%q", *g.kmsKeyARN)
	}
}

func TestGlobalFlagsFromProfileStore_SecretPrecedenceOverLegacyEnv(t *testing.T) {
	t.Setenv("LEGACY_AK", "legacy-ak")
	t.Setenv("SECRET_AK", "secret-ak")

	s := cloudstic.ProfileStore{
		URI:               "s3:bucket/prefix",
		S3AccessKeyEnv:    "LEGACY_AK",
		S3AccessKeySecret: "env://SECRET_AK",
	}

	g, err := globalFlagsFromProfileStore(s)
	if err != nil {
		t.Fatalf("globalFlagsFromProfileStore: %v", err)
	}
	if *g.s3AccessKey != "secret-ak" {
		t.Fatalf("s3AccessKey=%q want secret-ak", *g.s3AccessKey)
	}
}

func TestGlobalFlagsFromProfileStore_InvalidSecretRefReturnsError(t *testing.T) {
	s := cloudstic.ProfileStore{
		URI:            "s3:bucket/prefix",
		PasswordSecret: "env:/bad-format",
	}

	_, err := globalFlagsFromProfileStore(s)
	if err == nil {
		t.Fatal("expected error for invalid secret ref")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected field context in error, got: %v", err)
	}
}

func TestRunStoreNew_InvalidURI(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{"cloudstic", "store", "new", "-profiles-file", profilesPath, "-name", "bad", "-uri", "garbage-no-scheme"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code == 0 {
		t.Fatal("expected non-zero exit code for invalid URI")
	}
	if !strings.Contains(errOut.String(), "invalid store URI") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
}

func TestRunStoreNew_InvalidName(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{"cloudstic", "store", "new", "-profiles-file", profilesPath, "-name", "bad name!", "-uri", "local:/tmp/store"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code == 0 {
		t.Fatal("expected non-zero exit code for invalid name")
	}
	if !strings.Contains(errOut.String(), "invalid store name") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
}

func TestRunStore_UnknownSubcommand(t *testing.T) {
	os.Args = []string{"cloudstic", "store", "unknown"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "Unknown store subcommand") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
}

func TestRunStoreShow_WithEncryption(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  enc-store:
    uri: s3:enc-bucket/path
    password_env: MY_PW
    encryption_key_env: MY_EK
    recovery_key_env: MY_RK
    kms_key_arn: arn:aws:kms:us-east-1:111:key/xyz
    kms_region: us-west-2
    kms_endpoint: https://kms.custom.endpoint
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}

	os.Args = []string{"cloudstic", "store", "show", "-profiles-file", profilesPath, "enc-store"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store show failed: %s", errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"Password Env (deprecated)",
		"MY_PW",
		"Encryption Key Env (deprecated)",
		"MY_EK",
		"Recovery Key Env (deprecated)",
		"MY_RK",
		"KMS Key ARN",
		"arn:aws:kms:us-east-1:111:key/xyz",
		"KMS Region",
		"us-west-2",
		"KMS Endpoint",
		"https://kms.custom.endpoint",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in show output:\n%s", want, got)
		}
	}
}

func TestRunStoreShow_WithSFTP(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  sftp-store:
    uri: sftp://user@host/path
    store_sftp_password_env: SFTP_PW_ENV
    store_sftp_key_env: SFTP_KEY_ENV
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}

	os.Args = []string{"cloudstic", "store", "show", "-profiles-file", profilesPath, "sftp-store"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store show failed: %s", errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"SFTP Password Env (deprecated)",
		"SFTP_PW_ENV",
		"SFTP Key Env (deprecated)",
		"SFTP_KEY_ENV",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in show output:\n%s", want, got)
		}
	}
}

func TestRunStoreShow_WithS3Env(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  s3env-store:
    uri: s3:env-bucket/path
    s3_access_key_env: AK_ENV
    s3_secret_key_env: SK_ENV
    s3_profile_env: PROF_ENV
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}

	os.Args = []string{"cloudstic", "store", "show", "-profiles-file", profilesPath, "s3env-store"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store show failed: %s", errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"S3 Access Key Env (deprecated)",
		"AK_ENV",
		"S3 Secret Key Env (deprecated)",
		"SK_ENV",
		"S3 Profile Env",
		"PROF_ENV",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in show output:\n%s", want, got)
		}
	}
}

func TestRunStoreNew_WithSFTPOptions(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{
		"cloudstic", "store", "new",
		"-profiles-file", profilesPath,
		"-name", "sftp-new",
		"-uri", "sftp://user@host/path",
		"-store-sftp-password-env", "SFTP_PW",
		"-store-sftp-key-env", "SFTP_KEY",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store new failed: %s", errOut.String())
	}

	// Verify via show.
	os.Args = []string{"cloudstic", "store", "show", "-profiles-file", profilesPath, "sftp-new"}
	out.Reset()
	errOut.Reset()
	if code := r.runStore(); code != 0 {
		t.Fatalf("store show failed: %s", errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"SFTP Password Secret",
		"env://SFTP_PW",
		"SFTP Key Secret",
		"env://SFTP_KEY",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in show output:\n%s", want, got)
		}
	}
}

func TestRunStoreNew_WithAllS3Options(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{
		"cloudstic", "store", "new",
		"-profiles-file", profilesPath,
		"-name", "full-s3",
		"-uri", "s3:bucket",
		"-s3-region", "eu-west-1",
		"-s3-endpoint", "https://custom.endpoint",
		"-s3-profile", "prod",
		"-s3-access-key-env", "AK",
		"-s3-secret-key-env", "SK",
		"-s3-profile-env", "PROFILE",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store new failed: %s", errOut.String())
	}

	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles: %v", err)
	}
	yaml := string(raw)
	for _, want := range []string{
		"s3_region: eu-west-1",
		"s3_endpoint: https://custom.endpoint",
		"s3_profile: prod",
		"s3_access_key_secret: env://AK",
		"s3_secret_key_secret: env://SK",
		"s3_profile_env: PROFILE",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("expected %q in YAML:\n%s", want, yaml)
		}
	}
}

func TestRunStoreNew_WithSecretRefFlags(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{
		"cloudstic", "store", "new",
		"-profiles-file", profilesPath,
		"-name", "secrets-store",
		"-uri", "s3:bucket",
		"-s3-access-key-secret", "env://AWS_ACCESS_KEY_ID",
		"-s3-secret-key-secret", "keychain://cloudstic/prod/s3-secret",
		"-password-secret", "keychain://cloudstic/prod/password",
		"-encryption-key-secret", "wincred://cloudstic/prod/encryption-key",
		"-recovery-key-secret", "secret-service://cloudstic/prod/recovery-key",
		"-store-sftp-password-secret", "env://STORE_SFTP_PASSWORD",
		"-store-sftp-key-secret", "env://STORE_SFTP_KEY",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store new failed: %s", errOut.String())
	}

	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles: %v", err)
	}
	yaml := string(raw)
	for _, want := range []string{
		"s3_access_key_secret: env://AWS_ACCESS_KEY_ID",
		"s3_secret_key_secret: keychain://cloudstic/prod/s3-secret",
		"password_secret: keychain://cloudstic/prod/password",
		"encryption_key_secret: wincred://cloudstic/prod/encryption-key",
		"recovery_key_secret: secret-service://cloudstic/prod/recovery-key",
		"store_sftp_password_secret: env://STORE_SFTP_PASSWORD",
		"store_sftp_key_secret: env://STORE_SFTP_KEY",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("expected %q in YAML:\n%s", want, yaml)
		}
	}
}

func TestPromptSecretReferenceWithFns_DarwinKeychain(t *testing.T) {
	gotRef, err := promptSecretReferenceWithFns(
		"darwin",
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ string, _ []string) (string, error) { return "macOS Keychain (keychain://)", nil },
		func(label, def string) (string, error) { return def, nil },
		func(_ string) (string, error) { return "super-secret", nil },
		func(string) (string, bool) { return "", false },
		func(context.Context, string, string) (bool, error) { return false, nil },
		func(_ context.Context, service, account, value string) error {
			if service != "cloudstic/store/prod-store" {
				t.Fatalf("service=%q", service)
			}
			if account != "password" {
				t.Fatalf("account=%q", account)
			}
			if value != "super-secret" {
				t.Fatalf("value=%q", value)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("promptSecretReferenceWithFns: %v", err)
	}
	if gotRef != "keychain://cloudstic/store/prod-store/password" {
		t.Fatalf("ref=%q", gotRef)
	}
}

func TestPromptSecretReferenceWithFns_EnvFallback(t *testing.T) {
	gotRef, err := promptSecretReferenceWithFns(
		"darwin",
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ string, _ []string) (string, error) { return "Environment variable (env://)", nil },
		func(label, def string) (string, error) {
			if label != "Env var name" {
				t.Fatalf("unexpected label: %s", label)
			}
			return def, nil
		},
		func(_ string) (string, error) {
			t.Fatal("promptSecret should not be called")
			return "", nil
		},
		func(string) (string, bool) { return "", true },
		func(context.Context, string, string) (bool, error) {
			t.Fatal("nativeSecretExists should not be called")
			return false, nil
		},
		func(context.Context, string, string, string) error {
			t.Fatal("writeNativeSecret should not be called")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("promptSecretReferenceWithFns: %v", err)
	}
	if gotRef != "env://CLOUDSTIC_PASSWORD" {
		t.Fatalf("ref=%q", gotRef)
	}
}

func TestPromptSecretReferenceWithFns_KeychainWriteError(t *testing.T) {
	_, err := promptSecretReferenceWithFns(
		"darwin",
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ string, _ []string) (string, error) { return "macOS Keychain (keychain://)", nil },
		func(_ string, def string) (string, error) { return def, nil },
		func(_ string) (string, error) { return "secret", nil },
		func(string) (string, bool) { return "", false },
		func(context.Context, string, string) (bool, error) { return false, nil },
		func(context.Context, string, string, string) error { return errors.New("write failed") },
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPromptSecretReferenceWithFns_EmptySecret(t *testing.T) {
	_, err := promptSecretReferenceWithFns(
		"darwin",
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ string, _ []string) (string, error) { return "macOS Keychain (keychain://)", nil },
		func(_ string, def string) (string, error) { return def, nil },
		func(_ string) (string, error) { return "", nil },
		func(string) (string, bool) { return "", false },
		func(context.Context, string, string) (bool, error) { return false, nil },
		func(context.Context, string, string, string) error { return nil },
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPromptSecretReferenceWithFns_DarwinKeychainAdoptsExisting(t *testing.T) {
	gotRef, err := promptSecretReferenceWithFns(
		"darwin",
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ string, _ []string) (string, error) { return "macOS Keychain (keychain://)", nil },
		func(_ string, def string) (string, error) { return def, nil },
		func(_ string) (string, error) {
			t.Fatal("promptSecret should not be called when key exists")
			return "", nil
		},
		func(string) (string, bool) { return "", false },
		func(_ context.Context, service, account string) (bool, error) {
			if service != "cloudstic/store/prod-store" {
				t.Fatalf("service=%q", service)
			}
			if account != "password" {
				t.Fatalf("account=%q", account)
			}
			return true, nil
		},
		func(context.Context, string, string, string) error {
			t.Fatal("writeNativeSecret should not be called when key exists")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("promptSecretReferenceWithFns: %v", err)
	}
	if gotRef != "keychain://cloudstic/store/prod-store/password" {
		t.Fatalf("ref=%q", gotRef)
	}
}

func TestPromptSecretReferenceWithFns_DarwinEnvUnsetSwitchesToKeychain(t *testing.T) {
	selectCall := 0
	gotRef, err := promptSecretReferenceWithFns(
		"darwin",
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ string, _ []string) (string, error) {
			selectCall++
			if selectCall == 1 {
				return "Environment variable (env://)", nil
			}
			return "Store in macOS Keychain instead (keychain://)", nil
		},
		func(label, def string) (string, error) {
			if label != "Env var name" {
				t.Fatalf("unexpected prompt line label: %s", label)
			}
			return "UNSET_PASSWORD", nil
		},
		func(_ string) (string, error) { return "secret-value", nil },
		func(string) (string, bool) { return "", false },
		func(context.Context, string, string) (bool, error) { return false, nil },
		func(context.Context, string, string, string) error { return nil },
	)
	if err != nil {
		t.Fatalf("promptSecretReferenceWithFns: %v", err)
	}
	if gotRef != "keychain://cloudstic/store/prod-store/password" {
		t.Fatalf("ref=%q", gotRef)
	}
}

func TestCheckOrInitStore_MissingSecretAllowed(t *testing.T) {
	cfg := &cloudstic.ProfilesConfig{Version: 1, Stores: map[string]cloudstic.ProfileStore{
		"test": {
			URI:            "local:/tmp/store",
			PasswordSecret: "env://MISSING_STORE_PASSWORD",
		},
	}}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if err := r.checkOrInitStore(cfg, "test", "profiles.yaml", checkOrInitOptions{allowMissingSecrets: true, warnOnMissingSecrets: true, offerInit: true}); err != nil {
		t.Fatalf("checkOrInitStore: %v", err)
	}
	if !strings.Contains(errOut.String(), "cloudstic store verify test") {
		t.Fatalf("expected follow-up hint in stderr, got: %s", errOut.String())
	}
}

func TestCheckOrInitStore_MissingSecretAllowedSilent(t *testing.T) {
	cfg := &cloudstic.ProfilesConfig{Version: 1, Stores: map[string]cloudstic.ProfileStore{
		"test": {
			URI:            "local:/tmp/store",
			PasswordSecret: "env://MISSING_STORE_PASSWORD",
		},
	}}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if err := r.checkOrInitStore(cfg, "test", "profiles.yaml", checkOrInitOptions{allowMissingSecrets: true, offerInit: true}); err != nil {
		t.Fatalf("checkOrInitStore: %v", err)
	}
	if errOut.String() != "" {
		t.Fatalf("expected silent skip for missing secrets, got: %s", errOut.String())
	}
}

func TestRunStoreVerify_MissingSecretFails(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	cfg := &cloudstic.ProfilesConfig{Version: 1, Stores: map[string]cloudstic.ProfileStore{
		"test": {
			URI:            "local:/tmp/store",
			PasswordSecret: "env://MISSING_VERIFY_PASSWORD",
		},
	}}
	if err := cloudstic.SaveProfilesFile(profilesPath, cfg); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	os.Args = []string{"cloudstic", "store", "verify", "-profiles-file", profilesPath, "test"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "could not resolve store credentials") {
		t.Fatalf("unexpected stderr: %s", errOut.String())
	}
}

func TestStoreHasExplicitEncryption(t *testing.T) {
	if storeHasExplicitEncryption(cloudstic.ProfileStore{}) {
		t.Fatal("expected false for empty store")
	}
	if !storeHasExplicitEncryption(cloudstic.ProfileStore{PasswordSecret: "env://CLOUDSTIC_PASSWORD"}) {
		t.Fatal("expected true when password secret is set")
	}
}

func TestHasStoreNewOverrideFlags(t *testing.T) {
	if hasStoreNewOverrideFlags(map[string]bool{"name": true}) {
		t.Fatal("name-only should not count as override")
	}
	if hasStoreNewOverrideFlags(map[string]bool{"profiles-file": true}) {
		t.Fatal("profiles-file-only should not count as override")
	}
	if hasStoreNewOverrideFlags(map[string]bool{"name": true, "profiles-file": true}) {
		t.Fatal("identity-only flags should not count as override")
	}
	if !hasStoreNewOverrideFlags(map[string]bool{"name": true, "uri": true}) {
		t.Fatal("uri should count as override")
	}
}

func TestExistingStoreInteractivePlan(t *testing.T) {
	tests := []struct {
		name          string
		canPrompt     bool
		hasOverrides  bool
		hasEncryption bool
		wantURI       bool
		wantAsk       bool
	}{
		{name: "no prompt", canPrompt: false, hasOverrides: false, hasEncryption: true, wantURI: false, wantAsk: false},
		{name: "has overrides", canPrompt: true, hasOverrides: true, hasEncryption: true, wantURI: false, wantAsk: false},
		{name: "interactive no encryption", canPrompt: true, hasOverrides: false, hasEncryption: false, wantURI: true, wantAsk: false},
		{name: "interactive with encryption", canPrompt: true, hasOverrides: false, hasEncryption: true, wantURI: true, wantAsk: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotURI, gotAsk := existingStoreInteractivePlan(tc.canPrompt, tc.hasOverrides, tc.hasEncryption)
			if gotURI != tc.wantURI || gotAsk != tc.wantAsk {
				t.Fatalf("got (uri=%v, ask=%v), want (uri=%v, ask=%v)", gotURI, gotAsk, tc.wantURI, tc.wantAsk)
			}
		})
	}
}

func TestConfigureStoreEncryptionSelection_Password(t *testing.T) {
	var out strings.Builder
	s, err := configureStoreEncryptionSelection(
		cloudstic.ProfileStore{},
		"prod",
		"Password (recommended for interactive use)",
		func(string, string, string, string) (string, error) { return "env://MY_BACKUP_PASSWORD", nil },
		func(string, string) (string, error) { return "", nil },
		&out,
	)
	if err != nil {
		t.Fatalf("configureStoreEncryptionSelection: %v", err)
	}
	if s.PasswordSecret != "env://MY_BACKUP_PASSWORD" {
		t.Fatalf("password secret=%q", s.PasswordSecret)
	}
	if !strings.Contains(out.String(), "Encryption: password via env://MY_BACKUP_PASSWORD") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestConfigureStoreEncryptionSelection_KMS(t *testing.T) {
	var out strings.Builder
	s, err := configureStoreEncryptionSelection(
		cloudstic.ProfileStore{},
		"prod",
		"AWS KMS key (enterprise)",
		func(string, string, string, string) (string, error) { return "", nil },
		func(label, def string) (string, error) {
			switch label {
			case "KMS key ARN":
				return "arn:aws:kms:us-east-1:123:key/abc", nil
			case "KMS region":
				return "us-east-1", nil
			default:
				return def, nil
			}
		},
		&out,
	)
	if err != nil {
		t.Fatalf("configureStoreEncryptionSelection: %v", err)
	}
	if s.KMSKeyARN == "" || s.KMSRegion != "us-east-1" {
		t.Fatalf("unexpected kms values: arn=%q region=%q", s.KMSKeyARN, s.KMSRegion)
	}
}

func TestConfigureStoreEncryptionSelection_NoEncryption(t *testing.T) {
	var out strings.Builder
	_, err := configureStoreEncryptionSelection(
		cloudstic.ProfileStore{},
		"prod",
		"No encryption (not recommended)",
		func(string, string, string, string) (string, error) { return "", nil },
		func(string, string) (string, error) { return "", nil },
		&out,
	)
	if err != nil {
		t.Fatalf("configureStoreEncryptionSelection: %v", err)
	}
	if !strings.Contains(out.String(), "Encryption: none") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestConfigureStoreEncryptionSelection_KMSError(t *testing.T) {
	_, err := configureStoreEncryptionSelection(
		cloudstic.ProfileStore{},
		"prod",
		"AWS KMS key (enterprise)",
		func(string, string, string, string) (string, error) { return "", nil },
		func(label, def string) (string, error) {
			if label == "KMS key ARN" {
				return "", nil
			}
			return def, nil
		},
		&strings.Builder{},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "KMS key ARN is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPromptSecretReference_EnvInteractive(t *testing.T) {
	t.Setenv("MY_ENV", "set-for-test")
	if runtime.GOOS == "darwin" {
		setInteractiveStdinLines(t, "1", "MY_ENV")
	} else {
		setInteractiveStdinLines(t, "MY_ENV")
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	got, err := r.promptSecretReference("prod", "repository password", "CLOUDSTIC_PASSWORD", "password")
	if err != nil {
		t.Fatalf("promptSecretReference: %v", err)
	}
	if got != "env://MY_ENV" {
		t.Fatalf("ref=%q want env://MY_ENV", got)
	}
}

func TestPromptEncryptionConfig_PasswordViaEnvRef(t *testing.T) {
	t.Setenv("MY_BACKUP_PASSWORD", "set-for-test")
	tmp := t.TempDir()
	profilesPath := filepath.Join(tmp, "profiles.yaml")
	cfg := &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"prod": {URI: "local:/tmp/store"},
		},
	}

	if runtime.GOOS == "darwin" {
		setInteractiveStdinLines(t, "1", "1", "MY_BACKUP_PASSWORD")
	} else {
		setInteractiveStdinLines(t, "1", "MY_BACKUP_PASSWORD")
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	r.promptEncryptionConfig(cfg, "prod", profilesPath)

	s := cfg.Stores["prod"]
	if s.PasswordSecret != "env://MY_BACKUP_PASSWORD" {
		t.Fatalf("password secret=%q", s.PasswordSecret)
	}
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	if !strings.Contains(string(raw), "password_secret: env://MY_BACKUP_PASSWORD") {
		t.Fatalf("expected saved password_secret in YAML:\n%s", string(raw))
	}
}

func setInteractiveStdinLines(t *testing.T, lines ...string) {
	t.Helper()
	orig := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	for _, line := range lines {
		_, _ = w.WriteString(line + "\n")
	}
	_ = w.Close()
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
}

func TestValidRefName(t *testing.T) {
	valid := []string{"abc", "a-b", "a.b", "a_b", "A1", "test-store.v2"}
	for _, name := range valid {
		if !validRefName.MatchString(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}

	invalid := []string{"", "-abc", ".abc", "_abc", "a b", "a!b", "a@b"}
	for _, name := range invalid {
		if validRefName.MatchString(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestRunStoreList_MultipleStores(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  alpha:
    uri: local:/tmp/alpha
  beta:
    uri: s3:beta-bucket
  gamma:
    uri: sftp://user@host/gamma
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}

	os.Args = []string{"cloudstic", "store", "list", "-profiles-file", profilesPath}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store list failed: %s", errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Stores") || !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") || !strings.Contains(got, "gamma") {
		t.Fatalf("expected table output with all stores:\n%s", got)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(got, name) {
			t.Fatalf("expected %q in list output:\n%s", name, got)
		}
	}
}

func TestRunStoreNew_LocalStore(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	os.Args = []string{
		"cloudstic", "store", "new",
		"-profiles-file", profilesPath,
		"-name", "local-bk",
		"-uri", "local:/tmp/backup",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runStore(); code != 0 {
		t.Fatalf("store new failed: %s", errOut.String())
	}
	if !strings.Contains(out.String(), `"local-bk" saved`) {
		t.Fatalf("unexpected store new output: %s", out.String())
	}

	// Verify YAML has the correct URI.
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles: %v", err)
	}
	if !strings.Contains(string(raw), "uri: local:/tmp/backup") {
		t.Fatalf("expected URI in YAML:\n%s", string(raw))
	}
}

func TestGlobalFlagsFromProfileStore_DefaultRegion(t *testing.T) {
	s := cloudstic.ProfileStore{
		URI: "s3:some-bucket",
	}
	g, err := globalFlagsFromProfileStore(s)
	if err != nil {
		t.Fatalf("globalFlagsFromProfileStore: %v", err)
	}
	if *g.s3Region != "us-east-1" {
		t.Fatalf("expected default region us-east-1, got %q", *g.s3Region)
	}
}

func TestGlobalFlagsFromProfileStore_SFTPFields(t *testing.T) {
	s := cloudstic.ProfileStore{
		URI:               "sftp://user@host/path",
		StoreSFTPPassword: "direct-pw",
		StoreSFTPKey:      "/path/to/key",
	}
	g, err := globalFlagsFromProfileStore(s)
	if err != nil {
		t.Fatalf("globalFlagsFromProfileStore: %v", err)
	}
	if *g.storeSFTPPassword != "direct-pw" {
		t.Fatalf("expected storeSFTPPassword=direct-pw, got %q", *g.storeSFTPPassword)
	}
	if *g.storeSFTPKey != "/path/to/key" {
		t.Fatalf("expected storeSFTPKey=/path/to/key, got %q", *g.storeSFTPKey)
	}
}
