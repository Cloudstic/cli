// Package sourceoauth holds the OAuth2 machinery shared by the Google Drive
// and OneDrive sources: the local-callback authorization-code flow and a
// token source that persists refreshed tokens.
//
// It lives under internal/ because it is an implementation detail of those two
// providers, not part of the source contract in pkg/source.
package sourceoauth

// Default OAuth client credentials, injected at build time via ldflags:
//
//	-X github.com/cloudstic/cli/internal/sourceoauth.DefaultGoogleClientID=...
//	-X github.com/cloudstic/cli/internal/sourceoauth.DefaultGoogleClientSecret=...
//	-X github.com/cloudstic/cli/internal/sourceoauth.DefaultOneDriveClientID=...
//
// These paths are mirrored in .goreleaser.yml. Keep the two in sync: the Go
// linker silently ignores -X for a symbol that does not exist, so a stale path
// yields release binaries with empty client IDs and cloud auth that fails only
// at runtime.
//
// Users can still override at runtime via environment variables
// (GOOGLE_APPLICATION_CREDENTIALS, ONEDRIVE_CLIENT_ID).
//
// OneDrive uses the public client flow (PKCE) and does not need a client secret.
var (
	DefaultGoogleClientID     string
	DefaultGoogleClientSecret string

	DefaultOneDriveClientID string
)
