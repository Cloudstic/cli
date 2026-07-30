package config_test

import (
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
)

// TestSourceURIForProvider_ParsesAndNamesNoDrive is the regression test for a bug
// that survived because the mapping lived in package main, where nothing typed it
// and no test could reach it.
//
// `auth login` built its URI as provider + "://auth", which was wrong for both
// providers in different ways: "google" is not a source scheme at all (Google's
// is "gdrive"), so it failed to parse; and "onedrive://auth" parsed but put
// "auth" in the host, which both cloud sources read as a *drive name* and
// resolve eagerly during construction — so authenticating depended on a drive
// called "auth" existing.
//
// Both halves are asserted here: the URI must parse, and it must carry no drive
// name.
func TestSourceURIForProvider_ParsesAndNamesNoDrive(t *testing.T) {
	cases := []struct {
		provider   string
		wantScheme string
	}{
		{config.ProviderGoogle, "gdrive"},
		{config.ProviderOneDrive, "onedrive"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			raw, err := config.SourceURIForProvider(tc.provider)
			if err != nil {
				t.Fatalf("SourceURIForProvider: %v", err)
			}

			uri, err := config.ParseSourceURI(raw)
			if err != nil {
				t.Fatalf("the URI for provider %q does not parse: %v", tc.provider, err)
			}
			if uri.Scheme != tc.wantScheme {
				t.Errorf("scheme = %q, want %q", uri.Scheme, tc.wantScheme)
			}
			if uri.Host != "" {
				t.Errorf("host = %q, want empty: a drive name here makes signing in "+
					"depend on that drive existing", uri.Host)
			}
		})
	}
}

func TestSourceURIForProvider_Rejects(t *testing.T) {
	cases := []struct{ provider, want string }{
		{"", "names no provider"},
		{"gdrive", `unknown auth provider "gdrive"`}, // the scheme, not the provider
		{"dropbox", `unknown auth provider "dropbox"`},
		{"Google", `unknown auth provider "Google"`}, // matching is exact
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			_, err := config.SourceURIForProvider(tc.provider)
			if err == nil {
				t.Fatalf("expected an error for provider %q", tc.provider)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestSourceForAuth(t *testing.T) {
	t.Run("google", func(t *testing.T) {
		got, err := config.SourceForAuth(profile.Auth{
			Provider:        config.ProviderGoogle,
			GoogleCreds:     "/creds.json",
			GoogleCredsRef:  "keychain://creds",
			GoogleCredsJSON: `{"x":1}`,
			GoogleTokenFile: "/token.json",
			GoogleTokenRef:  "config-token://google/work",
		})
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		if got.URI != "gdrive" {
			t.Errorf("URI = %q, want gdrive", got.URI)
		}
		want := config.Google{
			CredsPath: "/creds.json",
			CredsRef:  "keychain://creds",
			CredsJSON: `{"x":1}`,
			TokenPath: "/token.json",
			TokenRef:  "config-token://google/work",
		}
		if got.Google != want {
			t.Errorf("Google = %+v, want %+v", got.Google, want)
		}
	})

	t.Run("onedrive", func(t *testing.T) {
		got, err := config.SourceForAuth(profile.Auth{
			Provider:          config.ProviderOneDrive,
			OneDriveClientID:  "client-1",
			OneDriveTokenFile: "/ms.json",
			OneDriveTokenRef:  "config-token://onedrive/work",
		})
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		if got.URI != "onedrive" {
			t.Errorf("URI = %q, want onedrive", got.URI)
		}
		want := config.OneDrive{
			ClientID:  "client-1",
			TokenPath: "/ms.json",
			TokenRef:  "config-token://onedrive/work",
		}
		if got.OneDrive != want {
			t.Errorf("OneDrive = %+v, want %+v", got.OneDrive, want)
		}
	})

	// A hand-edited file may hold both providers' fields on one entry. Only the
	// named provider's credentials travel, so the source cannot try both.
	t.Run("carries only the named provider's credentials", func(t *testing.T) {
		got, err := config.SourceForAuth(profile.Auth{
			Provider:          config.ProviderGoogle,
			GoogleTokenFile:   "/token.json",
			OneDriveClientID:  "should-not-travel",
			OneDriveTokenFile: "/should-not-travel.json",
		})
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		if got.OneDrive != (config.OneDrive{}) {
			t.Errorf("OneDrive = %+v, want zero for a google entry", got.OneDrive)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		if _, err := config.SourceForAuth(profile.Auth{Provider: "dropbox"}); err == nil {
			t.Fatal("expected an error for an unknown provider")
		}
	})
}

func TestDefaultAuthTokenRef(t *testing.T) {
	cases := []struct{ provider, name, want string }{
		{"google", "work", "config-token://google/work"},
		{"onedrive", "personal", "config-token://onedrive/personal"},
		{"google", "", "config-token://google/default"},
	}
	for _, tc := range cases {
		if got := config.DefaultAuthTokenRef(tc.provider, tc.name); got != tc.want {
			t.Errorf("DefaultAuthTokenRef(%q, %q) = %q, want %q", tc.provider, tc.name, got, tc.want)
		}
	}
}
