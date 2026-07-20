package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestMergeProfileBackupArgs_AppliesProfileAndStore(t *testing.T) {
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	cfg := &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"s": {
				URI:         "s3:bucket/prefix",
				S3Region:    "eu-west-1",
				S3AccessKey: "AKIA",
				S3SecretKey: "SECRET",
			},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"p": {
				Source:      "local:/data",
				Store:       "s",
				Tags:        []string{"daily"},
				Excludes:    []string{"*.tmp"},
				IgnoreEmpty: true,
			},
		},
	}
	if err := cloudstic.SaveProfilesFile(profilesPath, cfg); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	base := &backupArgs{sourceURI: "gdrive", globalFlags: newTestGlobalFlags()}
	base.profilesFile = profilesPath

	eff, err := mergeProfileBackupArgs(base, "p", cfg.Profiles["p"], cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if eff.sourceURI != "local:/data" {
		t.Fatalf("sourceURI=%q want local:/data", eff.sourceURI)
	}
	if len(eff.tags) != 1 || eff.tags[0] != "daily" {
		t.Fatalf("tags=%v want [daily]", eff.tags)
	}
	if len(eff.excludes) != 1 || eff.excludes[0] != "*.tmp" {
		t.Fatalf("excludes=%v want [*.tmp]", eff.excludes)
	}
	if !eff.ignoreEmpty {
		t.Fatal("expected ignoreEmpty to be true")
	}

	// The store reaches the command through resolution, not by rewriting flags.
	resolved, err := resolveClientConfig(eff.globalFlags)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if resolved.store.uri != "s3:bucket/prefix" {
		t.Fatalf("store.uri=%q want s3:bucket/prefix", resolved.store.uri)
	}
	if resolved.store.s3.region != "eu-west-1" {
		t.Fatalf("s3.region=%q want eu-west-1", resolved.store.s3.region)
	}
	if resolved.store.s3.accessKey != "AKIA" {
		t.Fatalf("s3.accessKey=%q want AKIA", resolved.store.s3.accessKey)
	}
}

func TestMergeProfileBackupArgs_CLIFlagsWin(t *testing.T) {
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	cfg := &cloudstic.ProfilesConfig{
		Version:  1,
		Stores:   map[string]cloudstic.ProfileStore{"s": {URI: "s3:bucket"}},
		Profiles: map[string]cloudstic.BackupProfile{"p": {Source: "local:/profile", Store: "s"}},
	}
	if err := cloudstic.SaveProfilesFile(profilesPath, cfg); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	base := &backupArgs{
		sourceURI:   "local:/cli",
		globalFlags: testProvided(newTestGlobalFlags(), "source", "store"),
	}
	base.store = "local:/cli-store"
	base.profilesFile = profilesPath

	eff, err := mergeProfileBackupArgs(base, "p", cfg.Profiles["p"], cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if eff.sourceURI != "local:/cli" {
		t.Fatalf("sourceURI=%q want local:/cli", eff.sourceURI)
	}

	resolved, err := resolveClientConfig(eff.globalFlags)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if resolved.store.uri != "local:/cli-store" {
		t.Fatalf("store.uri=%q want local:/cli-store", resolved.store.uri)
	}
}

func TestMergeProfileBackupArgs_AppliesAuthRef(t *testing.T) {
	base := &backupArgs{
		sourceURI:   "gdrive-changes:/Docs",
		globalFlags: newTestGlobalFlags(),
	}
	cfg := &cloudstic.ProfilesConfig{
		Auth: map[string]cloudstic.ProfileAuth{
			"google-work": {
				Provider:        "google",
				GoogleCreds:     "/tmp/creds.json",
				GoogleTokenFile: "/tmp/google-work.json",
			},
		},
	}
	p := cloudstic.BackupProfile{Source: "gdrive-changes:/Docs", AuthRef: "google-work"}

	eff, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if eff.googleCreds != "/tmp/creds.json" {
		t.Fatalf("googleCreds=%q want /tmp/creds.json", eff.googleCreds)
	}
	if eff.googleTokenFile != "/tmp/google-work.json" {
		t.Fatalf("googleTokenFile=%q want /tmp/google-work.json", eff.googleTokenFile)
	}
}

func TestMergeProfileBackupArgs_AuthRefUnknownFails(t *testing.T) {
	base := &backupArgs{
		sourceURI:   "gdrive-changes:/Docs",
		globalFlags: newTestGlobalFlags(),
	}
	cfg := &cloudstic.ProfilesConfig{}
	p := cloudstic.BackupProfile{Source: "gdrive-changes:/Docs", AuthRef: "missing"}

	_, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err == nil {
		t.Fatal("expected error for unknown auth ref")
	}
}

func TestMergeProfileBackupArgs_AuthProviderMismatchFails(t *testing.T) {
	base := &backupArgs{
		sourceURI:   "gdrive-changes:/Docs",
		globalFlags: newTestGlobalFlags(),
	}
	cfg := &cloudstic.ProfilesConfig{
		Auth: map[string]cloudstic.ProfileAuth{
			"ms-work": {Provider: "onedrive", OneDriveTokenFile: "/tmp/ms.json"},
		},
	}
	p := cloudstic.BackupProfile{Source: "gdrive-changes:/Docs", AuthRef: "ms-work"}

	_, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err == nil {
		t.Fatal("expected provider mismatch error")
	}
}

func TestMergeProfileBackupArgs_CLIAuthRefOverridesProfile(t *testing.T) {
	base := &backupArgs{
		sourceURI:   "gdrive-changes:/Docs",
		authRef:     "google-alt",
		globalFlags: testProvided(newTestGlobalFlags(), "auth-ref"),
	}
	cfg := &cloudstic.ProfilesConfig{
		Auth: map[string]cloudstic.ProfileAuth{
			"google-work": {Provider: "google", GoogleTokenFile: "/tmp/work.json"},
			"google-alt":  {Provider: "google", GoogleTokenFile: "/tmp/alt.json"},
		},
	}
	p := cloudstic.BackupProfile{Source: "gdrive-changes:/Docs", AuthRef: "google-work"}

	eff, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if eff.googleTokenFile != "/tmp/alt.json" {
		t.Fatalf("googleTokenFile=%q want /tmp/alt.json", eff.googleTokenFile)
	}
}

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

func TestEnsureDefaultAuthRefForCloudBackup_CreatesDefaultEntry(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	a := &backupArgs{
		sourceURI:   "gdrive-changes:/Docs",
		globalFlags: &globalFlags{profilesFile: profilesPath},
	}
	if err := ensureDefaultAuthRefForCloudBackup(a); err != nil {
		t.Fatalf("ensureDefaultAuthRefForCloudBackup: %v", err)
	}
	if a.authRef != "google-default" {
		t.Fatalf("authRef=%q want google-default", a.authRef)
	}
	if a.googleTokenFile == "" {
		t.Fatal("expected googleTokenFile to be set")
	}
	if _, err := os.Stat(profilesPath); err != nil {
		t.Fatalf("expected profiles file to exist: %v", err)
	}
	cfg, err := cloudstic.LoadProfilesFile(profilesPath)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	auth, ok := cfg.Auth["google-default"]
	if !ok {
		t.Fatal("missing google-default auth entry")
	}
	if auth.Provider != "google" {
		t.Fatalf("provider=%q want google", auth.Provider)
	}
}

func TestEnsureDefaultAuthRefForCloudBackup_NonCloudNoop(t *testing.T) {
	a := &backupArgs{sourceURI: "local:/tmp", globalFlags: &globalFlags{profilesFile: filepath.Join(t.TempDir(), "profiles.yaml")}}
	if err := ensureDefaultAuthRefForCloudBackup(a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.authRef != "" {
		t.Fatalf("authRef=%q want empty", a.authRef)
	}
}

func TestApplyProfileAuthToBackupArgs_OneDrive(t *testing.T) {
	a := &backupArgs{
		sourceURI: "onedrive:/Documents",
	}
	auth := cloudstic.ProfileAuth{
		Provider:          "onedrive",
		OneDriveClientID:  "my-client-id",
		OneDriveTokenFile: "/tmp/onedrive.json",
	}
	if err := applyProfileAuthToBackupArgs(a, auth); err != nil {
		t.Fatalf("applyProfileAuthToBackupArgs: %v", err)
	}
	if a.onedriveClientID != "my-client-id" {
		t.Fatalf("onedriveClientID=%q want my-client-id", a.onedriveClientID)
	}
	if a.onedriveTokenFile != "/tmp/onedrive.json" {
		t.Fatalf("onedriveTokenFile=%q want /tmp/onedrive.json", a.onedriveTokenFile)
	}
}

func TestApplyProfileAuthToBackupArgs_LocalSourceFails(t *testing.T) {
	a := &backupArgs{
		sourceURI: "local:/data",
	}
	auth := cloudstic.ProfileAuth{Provider: "google"}
	err := applyProfileAuthToBackupArgs(a, auth)
	if err == nil {
		t.Fatal("expected error for local source")
	}
	if got := err.Error(); got != "auth refs are only valid for Google Drive and OneDrive sources" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyProfileAuthToBackupArgs_CLIFlagsPreserved(t *testing.T) {
	a := &backupArgs{
		sourceURI:       "gdrive:/Docs",
		googleTokenFile: "cli.json",
		globalFlags:     testProvided(&globalFlags{}, "google-token-file"),
	}
	auth := cloudstic.ProfileAuth{
		Provider:        "google",
		GoogleTokenFile: "/tmp/profile.json",
	}
	if err := applyProfileAuthToBackupArgs(a, auth); err != nil {
		t.Fatalf("applyProfileAuthToBackupArgs: %v", err)
	}
	if a.googleTokenFile != "cli.json" {
		t.Fatalf("googleTokenFile=%q want cli.json", a.googleTokenFile)
	}
}

func TestCloneGlobalFlags_Independence(t *testing.T) {
	orig := newTestGlobalFlags()
	orig.store = "original-store"
	orig.profile = "orig-profile"
	orig.profilesFile = "/tmp/orig-profiles.yaml"
	orig.s3Profile = "orig-s3-profile"

	clone := cloneGlobalFlags(orig)
	clone.store = "modified-store"
	clone.profile = "clone-profile"
	clone.profilesFile = "/tmp/clone-profiles.yaml"
	clone.s3Profile = "clone-s3-profile"

	if orig.store != "original-store" {
		t.Fatalf("original store=%q want original-store", orig.store)
	}
	if clone.store != "modified-store" {
		t.Fatalf("clone store=%q want modified-store", clone.store)
	}
	if orig.profile != "orig-profile" {
		t.Fatalf("original profile=%q want orig-profile", orig.profile)
	}
	if orig.profilesFile != "/tmp/orig-profiles.yaml" {
		t.Fatalf("original profilesFile=%q want /tmp/orig-profiles.yaml", orig.profilesFile)
	}
	if orig.s3Profile != "orig-s3-profile" {
		t.Fatalf("original s3Profile=%q want orig-s3-profile", orig.s3Profile)
	}
}

func applyTestProfileStore(t *testing.T, s cloudstic.ProfileStore, provided ...string) (clientConfig, error) {
	t.Helper()
	cfg := clientConfigFromFlags(newTestGlobalFlags())
	set := map[string]bool{}
	for _, name := range provided {
		set[name] = true
	}
	err := applyProfileStore(&cfg, s, func(name string) bool { return set[name] })
	return cfg, err
}

func TestApplyProfileStore_AllFields(t *testing.T) {
	t.Setenv("TEST_PASSWORD", "secret-pw")
	t.Setenv("TEST_ENC_KEY", "enc-key-val")
	t.Setenv("TEST_REC_KEY", "rec-key-val")

	cfg, err := applyTestProfileStore(t, cloudstic.ProfileStore{
		URI:                 "s3:my-bucket/prefix",
		S3Region:            "us-east-1",
		S3Endpoint:          "https://s3.example.com",
		S3Profile:           "prod",
		S3AccessKey:         "AKIATEST",
		S3SecretKey:         "SECRETTEST",
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
		{"store.uri", cfg.store.uri, "s3:my-bucket/prefix"},
		{"s3.region", cfg.store.s3.region, "us-east-1"},
		{"s3.endpoint", cfg.store.s3.endpoint, "https://s3.example.com"},
		{"s3.profile", cfg.store.s3.profile, "prod"},
		{"s3.accessKey", cfg.store.s3.accessKey, "AKIATEST"},
		{"s3.secretKey", cfg.store.s3.secretKey, "SECRETTEST"},
		{"sftp.password", cfg.store.sftp.password, "sftp-pw"},
		{"sftp.key", cfg.store.sftp.key, "/tmp/sftp.key"},
		{"unlock.password", cfg.unlock.password, "secret-pw"},
		{"unlock.encryptionKey", cfg.unlock.encryptionKey, "enc-key-val"},
		{"unlock.recoveryKey", cfg.unlock.recoveryKey, "rec-key-val"},
		{"kms.keyARN", cfg.unlock.kms.keyARN, "arn:aws:kms:us-east-1:123:key/abc"},
		{"kms.region", cfg.unlock.kms.region, "us-east-1"},
		{"kms.endpoint", cfg.unlock.kms.endpoint, "https://kms.example.com"},
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
	err := applyProfileStore(&cfg, cloudstic.ProfileStore{URI: "s3:profile-bucket"},
		func(name string) bool { return name == "store" })
	if err != nil {
		t.Fatalf("applyProfileStore: %v", err)
	}
	if cfg.store.uri != "local:/cli-store" {
		t.Fatalf("store.uri=%q want local:/cli-store", cfg.store.uri)
	}
}

func TestApplyProfileStore_SecretRef(t *testing.T) {
	t.Setenv("SECRET_PW", "from-secret-ref")

	cfg, err := applyTestProfileStore(t, cloudstic.ProfileStore{PasswordSecret: "env://SECRET_PW"})
	if err != nil {
		t.Fatalf("applyProfileStore: %v", err)
	}
	if cfg.unlock.password != "from-secret-ref" {
		t.Fatalf("unlock.password=%q want from-secret-ref", cfg.unlock.password)
	}
}

func TestApplyProfileStore_InvalidSecretRef(t *testing.T) {
	_, err := applyTestProfileStore(t, cloudstic.ProfileStore{PasswordSecret: "env:/invalid-format"})
	if err == nil {
		t.Fatal("expected error for invalid secret ref")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected field context in error, got: %v", err)
	}
}

// The merged args name the profile rather than carrying its store values, so
// that resolveClientConfig applies the store definition in one place.
func TestMergeProfileBackupArgs_NamesProfileWithoutRewritingFlags(t *testing.T) {
	base := &backupArgs{sourceURI: "", globalFlags: newTestGlobalFlags()}
	baseStore := base.store
	cfg := &cloudstic.ProfilesConfig{
		Stores:   map[string]cloudstic.ProfileStore{"s": {URI: "s3:profile-bucket"}},
		Profiles: map[string]cloudstic.BackupProfile{"p": {Source: "local:/data", Store: "s"}},
	}
	eff, err := mergeProfileBackupArgs(base, "p", cfg.Profiles["p"], cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if eff.profile != "p" {
		t.Fatalf("profile=%q want p", eff.profile)
	}
	if eff.store != baseStore {
		t.Fatalf("store=%q want it left at %q", eff.store, baseStore)
	}
	if base.profile != "" {
		t.Fatalf("base flags mutated: profile=%q", base.profile)
	}
}

func TestMergeProfileBackupArgs_EmptySourceFails(t *testing.T) {
	base := &backupArgs{
		sourceURI:   "",
		globalFlags: newTestGlobalFlags(),
	}
	cfg := &cloudstic.ProfilesConfig{}
	p := cloudstic.BackupProfile{Source: ""}

	_, err := mergeProfileBackupArgs(base, "empty", p, cfg)
	if err == nil {
		t.Fatal("expected error for empty source")
	}
}

func TestMergeProfileBackupArgs_UnknownStoreFails(t *testing.T) {
	base := &backupArgs{
		sourceURI:   "local:/data",
		globalFlags: newTestGlobalFlags(),
	}
	cfg := &cloudstic.ProfilesConfig{
		Stores: map[string]cloudstic.ProfileStore{},
	}
	p := cloudstic.BackupProfile{Source: "local:/data", Store: "missing"}

	_, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err == nil {
		t.Fatal("expected error for unknown store")
	}
}

func TestMergeProfileBackupArgs_SkipNativeFiles(t *testing.T) {
	base := &backupArgs{
		sourceURI:   "gdrive:/Docs",
		globalFlags: newTestGlobalFlags(),
	}
	cfg := &cloudstic.ProfilesConfig{}
	p := cloudstic.BackupProfile{Source: "gdrive:/Docs", SkipNativeFiles: true}

	eff, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if !eff.skipNativeFiles {
		t.Fatal("expected skipNativeFiles=true")
	}
}

func TestMergeProfileBackupArgs_ExcludeFile(t *testing.T) {
	base := &backupArgs{
		sourceURI:   "local:/data",
		globalFlags: newTestGlobalFlags(),
	}
	cfg := &cloudstic.ProfilesConfig{}
	p := cloudstic.BackupProfile{Source: "local:/data", ExcludeFile: "/tmp/excludes.txt"}

	eff, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if eff.excludeFile != "/tmp/excludes.txt" {
		t.Fatalf("excludeFile=%q want /tmp/excludes.txt", eff.excludeFile)
	}
}

func TestEnsureDefaultAuthRefForCloudBackup_OneDrive(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	a := &backupArgs{
		sourceURI:   "onedrive:/Documents",
		globalFlags: &globalFlags{profilesFile: profilesPath},
	}
	if err := ensureDefaultAuthRefForCloudBackup(a); err != nil {
		t.Fatalf("ensureDefaultAuthRefForCloudBackup: %v", err)
	}
	if a.authRef != "onedrive-default" {
		t.Fatalf("authRef=%q want onedrive-default", a.authRef)
	}
}
