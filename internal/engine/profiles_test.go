package engine

import (
	"os"
	"path/filepath"
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
			"s": {URI: "local:./store"},
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
}
