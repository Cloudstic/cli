package main

import (
	"os"
	"path/filepath"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestApplyProfileStoreOverrides_AppliesProfileStore(t *testing.T) {
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

	os.Args = []string{"cloudstic", "list", "-profile", "p", "-profiles-file", profilesPath}
	g := newTestGlobalFlags()
	*g.profile = "p"
	*g.profilesFile = profilesPath

	if err := g.applyProfileStoreOverrides(); err != nil {
		t.Fatalf("applyProfileStoreOverrides: %v", err)
	}
	if *g.store != "s3:bucket/prefix" {
		t.Fatalf("store=%q want s3:bucket/prefix", *g.store)
	}
	if *g.s3Region != "eu-west-1" {
		t.Fatalf("s3Region=%q want eu-west-1", *g.s3Region)
	}
	if *g.s3Profile != "prod" {
		t.Fatalf("s3Profile=%q want prod", *g.s3Profile)
	}
}

func TestApplyProfileStoreOverrides_EncryptionFields(t *testing.T) {
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

	os.Args = []string{"cloudstic", "list", "-profile", "p", "-profiles-file", profilesPath}
	g := newTestGlobalFlags()
	*g.profile = "p"
	*g.profilesFile = profilesPath

	if err := g.applyProfileStoreOverrides(); err != nil {
		t.Fatalf("applyProfileStoreOverrides: %v", err)
	}
	if *g.password != "s3cret" {
		t.Fatalf("password=%q want s3cret", *g.password)
	}
	if *g.encryptionKey != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("encryptionKey=%q want aaa...", *g.encryptionKey)
	}
	if *g.recoveryKey == "" {
		t.Fatal("expected recoveryKey to be set from env")
	}
	if *g.kmsKeyARN != "arn:aws:kms:us-east-1:123456:key/abcd" {
		t.Fatalf("kmsKeyARN=%q want arn:...", *g.kmsKeyARN)
	}
	if *g.kmsRegion != "us-east-1" {
		t.Fatalf("kmsRegion=%q want us-east-1", *g.kmsRegion)
	}
	if *g.kmsEndpoint != "https://kms.example.com" {
		t.Fatalf("kmsEndpoint=%q want https://kms.example.com", *g.kmsEndpoint)
	}
}

func TestApplyProfileStoreOverrides_DoesNotOverrideExplicitStoreFlag(t *testing.T) {
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

	os.Args = []string{"cloudstic", "list", "-profile", "p", "-profiles-file", profilesPath, "-store", "local:/explicit"}
	g := newTestGlobalFlags()
	*g.profile = "p"
	*g.profilesFile = profilesPath
	*g.store = "local:/explicit"

	if err := g.applyProfileStoreOverrides(); err != nil {
		t.Fatalf("applyProfileStoreOverrides: %v", err)
	}
	if *g.store != "local:/explicit" {
		t.Fatalf("store=%q want local:/explicit", *g.store)
	}
}
