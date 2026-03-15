package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
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
	if !strings.Contains(out.String(), "1 stores") || !strings.Contains(out.String(), "prod-s3") {
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
	if !strings.Contains(got, "store: prod-s3") {
		t.Fatalf("expected store name in show output:\n%s", got)
	}
	if !strings.Contains(got, "uri: s3:my-bucket/backups") {
		t.Fatalf("expected URI in show output:\n%s", got)
	}
	if !strings.Contains(got, "s3_region: eu-west-1") {
		t.Fatalf("expected region in show output:\n%s", got)
	}
	if !strings.Contains(got, "s3_profile: prod") {
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
	if !strings.Contains(got, "used_by:") || !strings.Contains(got, "docs") || !strings.Contains(got, "photos") {
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
		"password_secret: env://MY_BACKUP_PASSWORD",
		"kms_key_arn: arn:aws:kms:us-east-1:123456:key/abcd",
		"kms_region: us-east-1",
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
	r.checkOrInitStore(cfg, "test", profilesPath)

	if !strings.Contains(out.String(), "already initialized") {
		t.Fatalf("expected 'already initialized' in output, got:\n%s", out.String())
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
		"password_env (deprecated): MY_PW",
		"encryption_key_env (deprecated): MY_EK",
		"recovery_key_env (deprecated): MY_RK",
		"kms_key_arn: arn:aws:kms:us-east-1:111:key/xyz",
		"kms_region: us-west-2",
		"kms_endpoint: https://kms.custom.endpoint",
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
		"store_sftp_password_env (deprecated): SFTP_PW_ENV",
		"store_sftp_key_env (deprecated): SFTP_KEY_ENV",
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
		"s3_access_key_env (deprecated): AK_ENV",
		"s3_secret_key_env (deprecated): SK_ENV",
		"s3_profile_env: PROF_ENV",
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
		"store_sftp_password_secret: env://SFTP_PW",
		"store_sftp_key_secret: env://SFTP_KEY",
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
	if !strings.Contains(got, "3 stores") {
		t.Fatalf("expected '3 stores' in output:\n%s", got)
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
