package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The tests below cover the profile helpers that had no coverage, as the
// safety net for moving this code out of internal/engine. They pin current
// behaviour rather than asserting anything new, so the move can be judged
// behaviour-preserving.

// IsEnabled treats a missing `enabled` key as true: a profile without the
// field must still run under -all-profiles, or adding the field later would
// silently change which profiles back up.
func TestBackupProfile_IsEnabled(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name string
		in   *bool
		want bool
	}{
		{"unset defaults to enabled", nil, true},
		{"explicitly enabled", &enabled, true},
		{"explicitly disabled", &disabled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (BackupProfile{Enabled: tc.in}).IsEnabled(); got != tc.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A missing file must read as an empty config, not an error: the caller
// distinguishing "no profiles file" from "unknown profile" depends on it.
func TestLoadProfilesFileOrEmpty_MissingFile(t *testing.T) {
	cfg, err := LoadProfilesFileOrEmpty(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing profiles file must not be an error: %v", err)
	}
	if cfg == nil || cfg.Version != 1 {
		t.Fatalf("got %+v, want an empty config at version 1", cfg)
	}
	if len(cfg.Profiles) != 0 {
		t.Errorf("expected no profiles, got %d", len(cfg.Profiles))
	}
}

// Any other read error must still surface — swallowing it would report a
// broken file as "no profiles".
func TestLoadProfilesFileOrEmpty_MalformedFileStillErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte("this: is: not: valid: yaml:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfilesFileOrEmpty(path); err == nil {
		t.Fatal("expected a malformed profiles file to error")
	} else if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a malformed file must not be reported as missing: %v", err)
	}
}

func TestEnsureProfilesMaps(t *testing.T) {
	cfg := &ProfilesConfig{}
	EnsureProfilesMaps(cfg)
	if cfg.Stores == nil || cfg.Profiles == nil || cfg.Auth == nil {
		t.Fatalf("all map fields must be non-nil, got %+v", cfg)
	}

	// Existing content must survive a second call.
	cfg.Profiles["keep"] = BackupProfile{}
	EnsureProfilesMaps(cfg)
	if _, ok := cfg.Profiles["keep"]; !ok {
		t.Error("EnsureProfilesMaps discarded existing entries")
	}
}

// Round-tripping is what the move must preserve end to end.
func TestSaveThenLoadProfilesFile_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	want := &ProfilesConfig{
		Version: 1,
		Stores:  map[string]ProfileStore{"s3": {URI: "local:///tmp/repo"}},
		Profiles: map[string]BackupProfile{
			"docs": {Source: "local:///home/me/docs", Store: "s3"},
		},
	}
	if err := SaveProfilesFile(path, want); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	got, err := LoadProfilesFile(path)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	if got.Profiles["docs"].Source != want.Profiles["docs"].Source {
		t.Errorf("Source = %q, want %q", got.Profiles["docs"].Source, want.Profiles["docs"].Source)
	}
	if got.Stores["s3"].URI != want.Stores["s3"].URI {
		t.Errorf("store URI = %q, want %q", got.Stores["s3"].URI, want.Stores["s3"].URI)
	}
}
