package config

import (
	"fmt"

	"github.com/cloudstic/cli/pkg/profile"
)

// Auth providers, as written in a profiles file's `provider:` field.
const (
	ProviderGoogle   = "google"
	ProviderOneDrive = "onedrive"
)

// SourceURIForProvider returns the source URI that authenticates a provider
// without addressing any particular content.
//
// The provider name and the source scheme are not the same word — Google's
// provider is "google" but its scheme is "gdrive" — and the URI deliberately
// carries no drive name or path. Both cloud sources resolve a drive name
// eagerly during construction, so naming one here would make authenticating
// depend on a drive existing, which is backwards: you authenticate in order to
// find out which drives there are.
func SourceURIForProvider(provider string) (string, error) {
	switch provider {
	case ProviderGoogle:
		return "gdrive", nil
	case ProviderOneDrive:
		return "onedrive", nil
	case "":
		return "", fmt.Errorf("auth entry names no provider: want %q or %q", ProviderGoogle, ProviderOneDrive)
	default:
		return "", fmt.Errorf("unknown auth provider %q: want %q or %q", provider, ProviderGoogle, ProviderOneDrive)
	}
}

// ProviderForSourceURI returns the auth provider a source URI needs, or empty
// when it needs none — a local or SFTP source, or a URI that does not parse.
//
// It is the inverse of SourceURIForProvider, and the single answer to "which
// provider does this source authenticate against". That question was previously
// answered by two switches over the same schemes: one here, inside applyAuth,
// and one in the CLI as profileProviderFromSource, which the TUI and the
// profiles-file renderer also called. A scheme added to ParseSourceURI had to
// reach both.
func ProviderForSourceURI(raw string) string {
	uri, err := ParseSourceURI(raw)
	if err != nil {
		return ""
	}
	switch uri.Scheme {
	case "gdrive", "gdrive-changes":
		return ProviderGoogle
	case "onedrive", "onedrive-changes":
		return ProviderOneDrive
	default:
		return ""
	}
}

// AuthProviderMatches reports whether an auth entry may be used with a source
// that requires the given provider.
//
// An entry naming no provider matches any source. The `provider` field
// postdates auth entries, so a hand-written or older profiles file may omit it,
// and refusing those would reject configurations that work today.
//
// That permissiveness is why this is a function rather than an inline
// comparison. It was written out twice — once resolving a profile for a backup
// (applyAuth) and once validating a profile before saving it
// (cmd/cloudstic's `profile new`) — in opposite directions, with different
// messages and different tests, so dropping the `== ""` half in one copy would
// have gone unnoticed (#584).
func AuthProviderMatches(required string, auth profile.Auth) bool {
	return auth.Provider == "" || auth.Provider == required
}

// SourceForAuth returns the source configuration that authenticates auth's
// provider with auth's credentials.
//
// Use it to act on a profiles file's auth entry — signing in, or checking which
// account an entry belongs to — without addressing any content: pass the result
// to open.Source and read the resulting source's Info.
//
// Only the credentials belonging to auth's provider are carried over. An entry
// holding both providers' fields (which a hand-edited file may) does not produce
// a source that would try both.
func SourceForAuth(auth profile.Auth) (Source, error) {
	uri, err := SourceURIForProvider(auth.Provider)
	if err != nil {
		return Source{}, err
	}

	cfg := Source{URI: uri}
	switch auth.Provider {
	case ProviderGoogle:
		cfg.Google = Google{
			CredsPath: auth.GoogleCreds,
			CredsRef:  auth.GoogleCredsRef,
			CredsJSON: auth.GoogleCredsJSON,
			TokenPath: auth.GoogleTokenFile,
			TokenRef:  auth.GoogleTokenRef,
		}
	case ProviderOneDrive:
		cfg.OneDrive = OneDrive{
			ClientID:  auth.OneDriveClientID,
			TokenPath: auth.OneDriveTokenFile,
			TokenRef:  auth.OneDriveTokenRef,
		}
	}
	return cfg, nil
}

// DefaultAuthTokenRef returns the secret reference an auth entry's OAuth token
// is stored under when the user names no location: a token in the config
// directory's managed store, keyed by provider and entry name.
//
// An empty name yields the "default" entry, which is what an auth entry created
// implicitly by `cloudstic backup gdrive` uses.
func DefaultAuthTokenRef(provider, name string) string {
	if name == "" {
		name = "default"
	}
	return "config-token://" + provider + "/" + name
}
