package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestMergeProfileBackupArgs_AppliesProfileAndStore(t *testing.T) {
	base := &backupArgs{
		sourceURI: "gdrive",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{},
	}
	cfg := &cloudstic.ProfilesConfig{
		Stores: map[string]cloudstic.ProfileStore{
			"s": {
				URI:         "s3:bucket/prefix",
				S3Region:    "eu-west-1",
				S3AccessKey: "AKIA",
				S3SecretKey: "SECRET",
			},
		},
	}
	p := cloudstic.BackupProfile{
		Source:      "local:/data",
		Store:       "s",
		Tags:        []string{"daily"},
		Excludes:    []string{"*.tmp"},
		IgnoreEmpty: true,
	}

	eff, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if eff.sourceURI != "local:/data" {
		t.Fatalf("sourceURI=%q want local:/data", eff.sourceURI)
	}
	if *eff.g.store != "s3:bucket/prefix" {
		t.Fatalf("store=%q want s3:bucket/prefix", *eff.g.store)
	}
	if *eff.g.s3Region != "eu-west-1" {
		t.Fatalf("s3Region=%q want eu-west-1", *eff.g.s3Region)
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
}

func TestMergeProfileBackupArgs_CLIFlagsWin(t *testing.T) {
	base := &backupArgs{
		sourceURI: "local:/cli",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{"source": true, "store": true},
	}
	*base.g.store = "local:/cli-store"

	cfg := &cloudstic.ProfilesConfig{
		Stores: map[string]cloudstic.ProfileStore{"s": {URI: "s3:bucket"}},
	}
	p := cloudstic.BackupProfile{Source: "local:/profile", Store: "s"}

	eff, err := mergeProfileBackupArgs(base, "p", p, cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs: %v", err)
	}
	if eff.sourceURI != "local:/cli" {
		t.Fatalf("sourceURI=%q want local:/cli", eff.sourceURI)
	}
	if *eff.g.store != "local:/cli-store" {
		t.Fatalf("store=%q want local:/cli-store", *eff.g.store)
	}
}

func TestMergeProfileBackupArgs_AppliesAuthRef(t *testing.T) {
	base := &backupArgs{
		sourceURI: "gdrive-changes:/Docs",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{},
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
		sourceURI: "gdrive-changes:/Docs",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{},
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
		sourceURI: "gdrive-changes:/Docs",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{},
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
		sourceURI: "gdrive-changes:/Docs",
		authRef:   "google-alt",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{"auth-ref": true},
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

func newTestGlobalFlags() *globalFlags {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	return addGlobalFlags(fs)
}

func TestEnsureDefaultAuthRefForCloudBackup_CreatesDefaultEntry(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	a := &backupArgs{
		sourceURI:    "gdrive-changes:/Docs",
		profilesFile: profilesPath,
		flagsSet:     map[string]bool{},
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
	a := &backupArgs{sourceURI: "local:/tmp", profilesFile: filepath.Join(t.TempDir(), "profiles.yaml"), flagsSet: map[string]bool{}}
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
		flagsSet:  map[string]bool{},
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
		flagsSet:  map[string]bool{},
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
		flagsSet:        map[string]bool{"google-token-file": true},
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
	*orig.store = "original-store"
	*orig.profile = "orig-profile"
	*orig.profilesFile = "/tmp/orig-profiles.yaml"
	*orig.s3Profile = "orig-s3-profile"

	clone := cloneGlobalFlags(orig)
	*clone.store = "modified-store"
	*clone.profile = "clone-profile"
	*clone.profilesFile = "/tmp/clone-profiles.yaml"
	*clone.s3Profile = "clone-s3-profile"

	if *orig.store != "original-store" {
		t.Fatalf("original store=%q want original-store", *orig.store)
	}
	if *clone.store != "modified-store" {
		t.Fatalf("clone store=%q want modified-store", *clone.store)
	}
	if *orig.profile != "orig-profile" {
		t.Fatalf("original profile=%q want orig-profile", *orig.profile)
	}
	if *orig.profilesFile != "/tmp/orig-profiles.yaml" {
		t.Fatalf("original profilesFile=%q want /tmp/orig-profiles.yaml", *orig.profilesFile)
	}
	if *orig.s3Profile != "orig-s3-profile" {
		t.Fatalf("original s3Profile=%q want orig-s3-profile", *orig.s3Profile)
	}
}

func TestApplyProfileStoreToGlobalFlags_AllFields(t *testing.T) {
	t.Setenv("TEST_PASSWORD", "secret-pw")
	t.Setenv("TEST_ENC_KEY", "enc-key-val")
	t.Setenv("TEST_REC_KEY", "rec-key-val")

	g := newTestGlobalFlags()
	flagsSet := map[string]bool{}
	s := cloudstic.ProfileStore{
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
	}

	if err := applyProfileStoreToGlobalFlags(g, s, flagsSet); err != nil {
		t.Fatalf("applyProfileStoreToGlobalFlags: %v", err)
	}

	if *g.store != "s3:my-bucket/prefix" {
		t.Fatalf("store=%q want s3:my-bucket/prefix", *g.store)
	}
	if *g.s3Region != "us-east-1" {
		t.Fatalf("s3Region=%q want us-east-1", *g.s3Region)
	}
	if *g.s3Endpoint != "https://s3.example.com" {
		t.Fatalf("s3Endpoint=%q want https://s3.example.com", *g.s3Endpoint)
	}
	if *g.s3Profile != "prod" {
		t.Fatalf("s3Profile=%q want prod", *g.s3Profile)
	}
	if *g.s3AccessKey != "AKIATEST" {
		t.Fatalf("s3AccessKey=%q want AKIATEST", *g.s3AccessKey)
	}
	if *g.s3SecretKey != "SECRETTEST" {
		t.Fatalf("s3SecretKey=%q want SECRETTEST", *g.s3SecretKey)
	}
	if *g.storeSFTPPassword != "sftp-pw" {
		t.Fatalf("storeSFTPPassword=%q want sftp-pw", *g.storeSFTPPassword)
	}
	if *g.storeSFTPKey != "/tmp/sftp.key" {
		t.Fatalf("storeSFTPKey=%q want /tmp/sftp.key", *g.storeSFTPKey)
	}
	if *g.password != "secret-pw" {
		t.Fatalf("password=%q want secret-pw", *g.password)
	}
	if *g.encryptionKey != "enc-key-val" {
		t.Fatalf("encryptionKey=%q want enc-key-val", *g.encryptionKey)
	}
	if *g.recoveryKey != "rec-key-val" {
		t.Fatalf("recoveryKey=%q want rec-key-val", *g.recoveryKey)
	}
	if *g.kmsKeyARN != "arn:aws:kms:us-east-1:123:key/abc" {
		t.Fatalf("kmsKeyARN=%q want arn:aws:kms:us-east-1:123:key/abc", *g.kmsKeyARN)
	}
	if *g.kmsRegion != "us-east-1" {
		t.Fatalf("kmsRegion=%q want us-east-1", *g.kmsRegion)
	}
	if *g.kmsEndpoint != "https://kms.example.com" {
		t.Fatalf("kmsEndpoint=%q want https://kms.example.com", *g.kmsEndpoint)
	}
}

func TestApplyProfileStoreToGlobalFlags_CLIFlagOverrides(t *testing.T) {
	g := newTestGlobalFlags()
	*g.store = "local:/cli-store"
	flagsSet := map[string]bool{"store": true}
	s := cloudstic.ProfileStore{URI: "s3:profile-bucket"}

	if err := applyProfileStoreToGlobalFlags(g, s, flagsSet); err != nil {
		t.Fatalf("applyProfileStoreToGlobalFlags: %v", err)
	}

	if *g.store != "local:/cli-store" {
		t.Fatalf("store=%q want local:/cli-store", *g.store)
	}
}

func TestApplyProfileStoreToGlobalFlags_SecretRef(t *testing.T) {
	t.Setenv("SECRET_PW", "from-secret-ref")

	g := newTestGlobalFlags()
	flagsSet := map[string]bool{}
	s := cloudstic.ProfileStore{
		PasswordSecret: "env://SECRET_PW",
	}

	if err := applyProfileStoreToGlobalFlags(g, s, flagsSet); err != nil {
		t.Fatalf("applyProfileStoreToGlobalFlags: %v", err)
	}
	if *g.password != "from-secret-ref" {
		t.Fatalf("password=%q want from-secret-ref", *g.password)
	}
}

func TestApplyProfileStoreToGlobalFlags_InvalidSecretRef(t *testing.T) {
	g := newTestGlobalFlags()
	flagsSet := map[string]bool{}
	s := cloudstic.ProfileStore{
		PasswordSecret: "env:/invalid-format",
	}

	err := applyProfileStoreToGlobalFlags(g, s, flagsSet)
	if err == nil {
		t.Fatal("expected error for invalid secret ref")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected field context in error, got: %v", err)
	}
}

func TestMergeProfileBackupArgs_EmptySourceFails(t *testing.T) {
	base := &backupArgs{
		sourceURI: "",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{},
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
		sourceURI: "local:/data",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{},
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
		sourceURI: "gdrive:/Docs",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{},
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
		sourceURI: "local:/data",
		g:         newTestGlobalFlags(),
		flagsSet:  map[string]bool{},
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
		sourceURI:    "onedrive:/Documents",
		profilesFile: profilesPath,
		flagsSet:     map[string]bool{},
	}
	if err := ensureDefaultAuthRefForCloudBackup(a); err != nil {
		t.Fatalf("ensureDefaultAuthRefForCloudBackup: %v", err)
	}
	if a.authRef != "onedrive-default" {
		t.Fatalf("authRef=%q want onedrive-default", a.authRef)
	}
}
