package config_test

import (
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
)

// These moved here with the fold they exercise, which used to be
// cmd_backup.go's mergeProfileBackupArgs and applyProfileAuthToBackupArgs — 140
// lines of hand-rolled precedence over the CLI's own flag struct (RFC 0022 §7).

// profilesWith builds a config holding one profile named "p".
func profilesWith(p profile.Profile, auth map[string]profile.Auth) *profile.Config {
	return &profile.Config{
		Version:  1,
		Profiles: map[string]profile.Profile{"p": p},
		Auth:     auth,
	}
}

func TestMergeProfileBackup_TakesProfileValues(t *testing.T) {
	cfg := profilesWith(profile.Profile{
		Source:          "local:/data",
		Tags:            []string{"nightly", "docs"},
		Excludes:        []string{"*.log"},
		ExcludeFile:     "/tmp/excludes.txt",
		IgnoreEmpty:     true,
		SkipNativeFiles: true,
		VolumeUUID:      "vol-1",
	}, nil)

	got, err := config.MergeProfileBackup(config.Backup{}, nil, "p", cfg)
	if err != nil {
		t.Fatalf("MergeProfileBackup: %v", err)
	}

	if got.Source.URI != "local:/data" {
		t.Errorf("source = %q, want the profile's", got.Source.URI)
	}
	if strings.Join(got.Tags, ",") != "nightly,docs" {
		t.Errorf("tags = %v, want the profile's", got.Tags)
	}
	if strings.Join(got.Source.Excludes, ",") != "*.log" {
		t.Errorf("excludes = %v, want the profile's", got.Source.Excludes)
	}
	if got.Source.ExcludeFile != "/tmp/excludes.txt" {
		t.Errorf("exclude file = %q, want the profile's", got.Source.ExcludeFile)
	}
	if !got.IgnoreEmpty {
		t.Error("ignore-empty not taken from the profile")
	}
	if !got.Source.SkipNativeFiles {
		t.Error("skip-native-files not taken from the profile")
	}
	if got.Source.VolumeUUID != "vol-1" {
		t.Errorf("volume UUID = %q, want the profile's", got.Source.VolumeUUID)
	}
}

func TestMergeProfileBackup_DecidedFieldsWin(t *testing.T) {
	cfg := profilesWith(profile.Profile{
		Source:      "local:/from-profile",
		ExcludeFile: "/from-profile.txt",
		VolumeUUID:  "profile-vol",
	}, nil)

	mine := config.Backup{Source: config.Source{
		URI:         "local:/from-flags",
		ExcludeFile: "/from-flags.txt",
	}}
	decided := config.NewFieldSet(config.FieldSourceURI, config.FieldExcludeFile)

	got, err := config.MergeProfileBackup(mine, decided, "p", cfg)
	if err != nil {
		t.Fatalf("MergeProfileBackup: %v", err)
	}
	if got.Source.URI != "local:/from-flags" {
		t.Errorf("source = %q, want the caller's", got.Source.URI)
	}
	if got.Source.ExcludeFile != "/from-flags.txt" {
		t.Errorf("exclude file = %q, want the caller's", got.Source.ExcludeFile)
	}
	// Undecided fields still come from the profile.
	if got.Source.VolumeUUID != "profile-vol" {
		t.Errorf("volume UUID = %q, want the profile's", got.Source.VolumeUUID)
	}
}

// The repeatable flags merge on emptiness, not on being decided: passing some
// means those instead of the profile's, and passing none takes the profile's.
func TestMergeProfileBackup_RepeatableFlagsReplaceRatherThanAppend(t *testing.T) {
	cfg := profilesWith(profile.Profile{
		Source:   "local:/data",
		Tags:     []string{"from-profile"},
		Excludes: []string{"from-profile"},
	}, nil)

	mine := config.Backup{
		Tags:   []string{"from-flags"},
		Source: config.Source{Excludes: []string{"from-flags"}},
	}
	got, err := config.MergeProfileBackup(mine, nil, "p", cfg)
	if err != nil {
		t.Fatalf("MergeProfileBackup: %v", err)
	}
	if strings.Join(got.Tags, ",") != "from-flags" {
		t.Errorf("tags = %v, want only the caller's", got.Tags)
	}
	if strings.Join(got.Source.Excludes, ",") != "from-flags" {
		t.Errorf("excludes = %v, want only the caller's", got.Source.Excludes)
	}
}

func TestMergeProfileBackup_Errors(t *testing.T) {
	cases := []struct {
		name string
		cfg  *profile.Config
		base config.Backup
		want string
	}{
		{
			name: "unknown profile",
			cfg:  profilesWith(profile.Profile{Source: "local:/data"}, nil),
			want: "unknown profile",
		},
		{
			name: "empty source",
			cfg:  profilesWith(profile.Profile{}, nil),
			want: "empty source",
		},
		{
			name: "unknown auth ref",
			cfg:  profilesWith(profile.Profile{Source: "gdrive:/Docs", AuthRef: "missing"}, nil),
			want: `unknown auth "missing"`,
		},
		{
			name: "auth provider mismatch",
			cfg: profilesWith(profile.Profile{Source: "gdrive-changes:/Docs", AuthRef: "ms"},
				map[string]profile.Auth{"ms": {Provider: "onedrive"}}),
			want: "provider mismatch",
		},
		{
			name: "auth ref on a local source",
			cfg: profilesWith(profile.Profile{Source: "local:/data", AuthRef: "g"},
				map[string]profile.Auth{"g": {Provider: "google"}}),
			want: "only valid for Google Drive and OneDrive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "p"
			if tc.name == "unknown profile" {
				name = "nope"
			}
			_, err := config.MergeProfileBackup(tc.base, nil, name, tc.cfg)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestMergeProfileBackup_AppliesAuthRef(t *testing.T) {
	t.Run("google", func(t *testing.T) {
		cfg := profilesWith(
			profile.Profile{Source: "gdrive-changes:/Docs", AuthRef: "google-work"},
			map[string]profile.Auth{"google-work": {
				Provider:        "google",
				GoogleCreds:     "/tmp/creds.json",
				GoogleTokenFile: "/tmp/google-work.json",
			}})

		got, err := config.MergeProfileBackup(config.Backup{}, nil, "p", cfg)
		if err != nil {
			t.Fatalf("MergeProfileBackup: %v", err)
		}
		if got.Source.Google.CredsPath != "/tmp/creds.json" {
			t.Errorf("creds = %q, want the auth entry's", got.Source.Google.CredsPath)
		}
		if got.Source.Google.TokenPath != "/tmp/google-work.json" {
			t.Errorf("token = %q, want the auth entry's", got.Source.Google.TokenPath)
		}
		if got.AuthRef != "google-work" {
			t.Errorf("AuthRef = %q, want the entry that was used recorded", got.AuthRef)
		}
	})

	t.Run("onedrive", func(t *testing.T) {
		cfg := profilesWith(
			profile.Profile{Source: "onedrive:/Docs", AuthRef: "ms"},
			map[string]profile.Auth{"ms": {
				Provider:          "onedrive",
				OneDriveClientID:  "client-1",
				OneDriveTokenFile: "/tmp/ms.json",
			}})

		got, err := config.MergeProfileBackup(config.Backup{}, nil, "p", cfg)
		if err != nil {
			t.Fatalf("MergeProfileBackup: %v", err)
		}
		if got.Source.OneDrive.ClientID != "client-1" {
			t.Errorf("client ID = %q, want the auth entry's", got.Source.OneDrive.ClientID)
		}
		if got.Source.OneDrive.TokenPath != "/tmp/ms.json" {
			t.Errorf("token = %q, want the auth entry's", got.Source.OneDrive.TokenPath)
		}
	})

	// A caller's own auth-ref beats the profile's, the same way any other
	// decided field does.
	t.Run("decided auth ref wins", func(t *testing.T) {
		cfg := profilesWith(
			profile.Profile{Source: "gdrive-changes:/Docs", AuthRef: "google-work"},
			map[string]profile.Auth{
				"google-work": {Provider: "google", GoogleTokenFile: "/tmp/work.json"},
				"google-alt":  {Provider: "google", GoogleTokenFile: "/tmp/alt.json"},
			})

		got, err := config.MergeProfileBackup(
			config.Backup{AuthRef: "google-alt"},
			config.NewFieldSet(config.FieldAuthRef), "p", cfg)
		if err != nil {
			t.Fatalf("MergeProfileBackup: %v", err)
		}
		if got.Source.Google.TokenPath != "/tmp/alt.json" {
			t.Errorf("token = %q, want the caller's auth entry", got.Source.Google.TokenPath)
		}
	})
}

// A decided credential is not replaced by the auth entry's, so passing a token
// path on the command line still wins over the entry that names another.
func TestApplyProfileAuth_DecidedCredentialsPreserved(t *testing.T) {
	base := config.Backup{Source: config.Source{
		URI:    "gdrive:/Docs",
		Google: config.Google{TokenPath: "/from-flags.json"},
	}}

	got, err := config.ApplyProfileAuth(base, config.NewFieldSet(config.FieldGoogleTokenFile),
		profile.Auth{Provider: "google", GoogleTokenFile: "/from-auth.json", GoogleCreds: "/creds.json"})
	if err != nil {
		t.Fatalf("ApplyProfileAuth: %v", err)
	}
	if got.Source.Google.TokenPath != "/from-flags.json" {
		t.Errorf("token = %q, want the caller's", got.Source.Google.TokenPath)
	}
	// What the caller did not decide still comes from the entry.
	if got.Source.Google.CredsPath != "/creds.json" {
		t.Errorf("creds = %q, want the auth entry's", got.Source.Google.CredsPath)
	}
}

func TestMergeProfileBackup_DoesNotMutateBase(t *testing.T) {
	base := config.Backup{Source: config.Source{URI: "local:/base"}}
	cfg := profilesWith(profile.Profile{Source: "local:/profile", VolumeUUID: "v"}, nil)

	if _, err := config.MergeProfileBackup(base, nil, "p", cfg); err != nil {
		t.Fatalf("MergeProfileBackup: %v", err)
	}
	if base.Source.URI != "local:/base" || base.Source.VolumeUUID != "" {
		t.Errorf("base was mutated: %+v", base.Source)
	}
}

func TestBackupFields_AreUniqueAndKeyed(t *testing.T) {
	seen := map[config.Field]bool{}
	for _, f := range config.BackupFields() {
		if seen[f] {
			t.Errorf("field %q listed twice", f)
		}
		seen[f] = true
	}
	if len(seen) == 0 {
		t.Fatal("BackupFields returned nothing; the tests above would pass vacuously")
	}
	// Store and backup fields must not collide: one FieldSet may name both, so a
	// shared value would make deciding one silently decide the other.
	for _, f := range config.StoreFields() {
		if seen[f] {
			t.Errorf("field %q is both a store field and a backup field", f)
		}
	}
}

// TestMergeProfileBackup_AuthEntryBeatsProfileCredentials pins the precedence
// between the two places a profiles file can state the same cloud credential.
//
// profile.Profile and profile.Auth both carry GoogleCreds, GoogleTokenFile and
// the rest, and a hand-edited file may set both. The auth entry wins: it is the
// deliberate, shared statement of where a provider's credentials live, while
// the profile's own copies predate auth entries. Before this test the
// precedence was an accident of statement order inside MergeProfileBackup.
func TestMergeProfileBackup_AuthEntryBeatsProfileCredentials(t *testing.T) {
	cfg := &profile.Config{
		Profiles: map[string]profile.Profile{
			"p": {
				Source:          "gdrive",
				AuthRef:         "a",
				GoogleCreds:     "/from/profile.json",
				GoogleTokenFile: "/from/profile-token.json",
			},
		},
		Auth: map[string]profile.Auth{
			"a": {
				Provider:        config.ProviderGoogle,
				GoogleCreds:     "/from/auth.json",
				GoogleTokenFile: "/from/auth-token.json",
			},
		},
	}

	got, err := config.MergeProfileBackup(config.Backup{}, nil, "p", cfg)
	if err != nil {
		t.Fatalf("MergeProfileBackup: %v", err)
	}
	if got.Source.Google.CredsPath != "/from/auth.json" {
		t.Errorf("auth entry must win over the profile's own copy, got %q", got.Source.Google.CredsPath)
	}
	if got.Source.Google.TokenPath != "/from/auth-token.json" {
		t.Errorf("auth entry must win over the profile's own copy, got %q", got.Source.Google.TokenPath)
	}
}

// TestMergeProfileBackup_ProfileCredentialSurvivesASilentAuthEntry is the other
// half: an auth entry that says nothing about a field must not blank the
// profile's value, which is what lets the two coexist rather than conflict.
func TestMergeProfileBackup_ProfileCredentialSurvivesASilentAuthEntry(t *testing.T) {
	cfg := &profile.Config{
		Profiles: map[string]profile.Profile{
			"p": {Source: "gdrive", AuthRef: "a", GoogleCreds: "/from/profile.json"},
		},
		Auth: map[string]profile.Auth{
			"a": {Provider: config.ProviderGoogle, GoogleTokenFile: "/from/auth-token.json"},
		},
	}

	got, err := config.MergeProfileBackup(config.Backup{}, nil, "p", cfg)
	if err != nil {
		t.Fatalf("MergeProfileBackup: %v", err)
	}
	if got.Source.Google.CredsPath != "/from/profile.json" {
		t.Errorf("a silent auth entry must not clear the profile's value, got %q", got.Source.Google.CredsPath)
	}
	if got.Source.Google.TokenPath != "/from/auth-token.json" {
		t.Errorf("the auth entry's own field must still apply, got %q", got.Source.Google.TokenPath)
	}
}
