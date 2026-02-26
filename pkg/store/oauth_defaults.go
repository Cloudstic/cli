package store

// Default OAuth client credentials, injected at build time via ldflags:
//
//	-X github.com/cloudstic/cli/pkg/store.defaultGoogleClientID=...
//	-X github.com/cloudstic/cli/pkg/store.defaultGoogleClientSecret=...
//	-X github.com/cloudstic/cli/pkg/store.defaultOneDriveClientID=...
//	-X github.com/cloudstic/cli/pkg/store.defaultOneDriveClientSecret=...
//
// Users can still override at runtime via environment variables
// (GOOGLE_APPLICATION_CREDENTIALS, ONEDRIVE_CLIENT_ID, ONEDRIVE_CLIENT_SECRET).
var (
	defaultGoogleClientID     string
	defaultGoogleClientSecret string

	defaultOneDriveClientID     string
	defaultOneDriveClientSecret string
)
