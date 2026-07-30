package config

import (
	"fmt"

	"github.com/cloudstic/cli/pkg/profile"
)

// Google holds the credentials for a Google Drive source. Credentials may be
// given as a file path, a secret reference, or inline JSON; the source tries
// them in that order.
type Google struct {
	CredsPath string
	CredsRef  string
	CredsJSON string
	TokenPath string
	TokenRef  string
}

// OneDrive holds the credentials for a OneDrive source.
type OneDrive struct {
	ClientID  string
	TokenPath string
	TokenRef  string
}

// Source is everything needed to construct a backup source.
//
// Which fields matter depends on the URI's scheme, the same way config.Store
// works: a local source reads VolumeUUID and the metadata switches, an SFTP
// source reads SFTP, and the cloud sources read Google or OneDrive.
type Source struct {
	URI string

	// ConfigDir locates the token files a cloud source falls back to when
	// Google.TokenPath or OneDrive.TokenPath is empty, and the managed store
	// behind config-token:// references. It carries paths.ConfigDir's meaning:
	// empty means CLOUDSTIC_CONFIG_DIR or the platform default.
	ConfigDir string

	SFTP     SFTP
	Google   Google
	OneDrive OneDrive

	// VolumeUUID overrides the detected volume identity of a local source,
	// which is what lets a portable drive back up incrementally from more than
	// one machine.
	VolumeUUID string

	// SkipNativeFiles excludes Google-native documents, which have no byte
	// stream to download.
	SkipNativeFiles bool

	SkipMode        bool
	SkipFlags       bool
	SkipXattrs      bool
	XattrNamespaces []string

	// Excludes are gitignore-syntax patterns. ExcludeFile names a file holding
	// more of them, one per line; open.Backup reads it and appends to Excludes.
	Excludes    []string
	ExcludeFile string
}

// Backup is the resolved configuration for one backup run: what to read, and
// how to record it.
//
// Zero values are the correct defaults, as elsewhere in this package: a Backup
// with only Source set behaves the way `cloudstic backup` behaves with no flags
// beyond -source.
type Backup struct {
	Source Source

	// Tags are recorded on the snapshot and are what `forget -tag` filters on.
	Tags []string

	// IgnoreEmpty skips writing a snapshot when nothing changed.
	IgnoreEmpty bool

	// AuthRef names the profiles-file auth entry that supplied the cloud
	// credentials in Source, or that should. MergeProfileBackup reads it and
	// records the entry it used, so the resolved configuration says where the
	// credentials came from.
	AuthRef string

	DryRun  bool
	Verbose bool
}

// The fields a profile can supply about what to back up, as distinct from where
// the repository is (see StoreFields). A field's string value is the cloudstic
// flag that carries it.
const (
	FieldSourceURI         Field = "source"
	FieldTags              Field = "tag"
	FieldExcludes          Field = "exclude"
	FieldExcludeFile       Field = "exclude-file"
	FieldIgnoreEmpty       Field = "ignore-empty-snapshot"
	FieldSkipNativeFiles   Field = "skip-native-files"
	FieldVolumeUUID        Field = "volume-uuid"
	FieldGoogleCreds       Field = "google-credentials"
	FieldGoogleCredsRef    Field = "google-credentials-ref"
	FieldGoogleCredsJSON   Field = "google-credentials-json"
	FieldGoogleTokenFile   Field = "google-token-file"
	FieldGoogleTokenRef    Field = "google-token-ref"
	FieldOneDriveClientID  Field = "onedrive-client-id"
	FieldOneDriveTokenFile Field = "onedrive-token-file"
	FieldOneDriveTokenRef  Field = "onedrive-token-ref"
	FieldAuthRef           Field = "auth-ref"
)

// backupFieldKeys maps each backup field to its profiles-file spelling, so an
// error can name the YAML entry rather than the flag. It is separate from
// fieldSpecs because these fields are not all strings — bools and string slices
// among them — and no single accessor shape fits, which is why the fold below is
// written out rather than driven from a table.
var backupFieldKeys = map[Field]string{
	FieldSourceURI:         "source",
	FieldTags:              "tags",
	FieldExcludes:          "excludes",
	FieldExcludeFile:       "exclude_file",
	FieldIgnoreEmpty:       "ignore_empty",
	FieldSkipNativeFiles:   "skip_native_files",
	FieldVolumeUUID:        "volume_uuid",
	FieldGoogleCreds:       "google_credentials",
	FieldGoogleCredsRef:    "google_credentials_ref",
	FieldGoogleCredsJSON:   "google_credentials_json",
	FieldGoogleTokenFile:   "google_token_file",
	FieldGoogleTokenRef:    "google_token_ref",
	FieldOneDriveClientID:  "onedrive_client_id",
	FieldOneDriveTokenFile: "onedrive_token_file",
	FieldOneDriveTokenRef:  "onedrive_token_ref",
	FieldAuthRef:           "auth_ref",
}

// BackupFields returns every field a profile can supply about what to back up,
// in a fresh slice. See StoreFields for the ones describing where it goes.
func BackupFields() []Field {
	return []Field{
		FieldSourceURI, FieldTags, FieldExcludes, FieldExcludeFile, FieldIgnoreEmpty,
		FieldSkipNativeFiles, FieldVolumeUUID,
		FieldGoogleCreds, FieldGoogleCredsRef, FieldGoogleCredsJSON,
		FieldGoogleTokenFile, FieldGoogleTokenRef,
		FieldOneDriveClientID, FieldOneDriveTokenFile, FieldOneDriveTokenRef,
		FieldAuthRef,
	}
}

// MergeProfileBackup returns base with profile name's backup settings folded in
// underneath it: every field the caller has already decided is left alone, and
// the rest come from the profile.
//
// decided names the fields the caller owns, using the same FieldSet as
// MergeProfileStore — one set may name both store and backup fields, since a
// caller usually resolves both from one mechanism.
//
// An auth_ref on the profile, or FieldAuthRef among the decided fields with
// base naming one, supplies the cloud credentials. Its provider must match the
// source's scheme: pointing a Google Drive source at a OneDrive auth entry is
// an error rather than a silently unauthenticated run.
//
// This is the counterpart to MergeProfileStore and applies the same precedence.
// The two are separate functions because a profile's store is shared between
// profiles by name while its backup settings are its own, so a caller resolving
// a store by name (`cloudstic store verify`) needs the first without the second.
func MergeProfileBackup(base Backup, decided FieldSet, name string, cfg *profile.Config) (Backup, error) {
	p, ok := cfg.Profiles[name]
	if !ok {
		return Backup{}, fmt.Errorf("unknown profile %q", name)
	}

	out := base

	// Taken even when the profile is silent: the caller asked for this profile,
	// so its source is the source. An empty result is reported below rather
	// than falling back to whatever base held, which would back up the wrong
	// tree under a profile's name.
	if !decided.Has(FieldSourceURI) {
		out.Source.URI = p.Source
	}
	if out.Source.URI == "" {
		return Backup{}, fmt.Errorf("profile %q has empty source", name)
	}
	if !decided.Has(FieldSkipNativeFiles) {
		out.Source.SkipNativeFiles = p.SkipNativeFiles
	}
	if !decided.Has(FieldIgnoreEmpty) {
		out.IgnoreEmpty = p.IgnoreEmpty
	}

	// Taken only when the profile names a value.
	for _, f := range []struct {
		field Field
		val   string
		dest  *string
	}{
		{FieldVolumeUUID, p.VolumeUUID, &out.Source.VolumeUUID},
		{FieldExcludeFile, p.ExcludeFile, &out.Source.ExcludeFile},
		{FieldGoogleCreds, p.GoogleCreds, &out.Source.Google.CredsPath},
		{FieldGoogleCredsRef, p.GoogleCredsRef, &out.Source.Google.CredsRef},
		{FieldGoogleCredsJSON, p.GoogleCredsJSON, &out.Source.Google.CredsJSON},
		{FieldGoogleTokenFile, p.GoogleTokenFile, &out.Source.Google.TokenPath},
		{FieldGoogleTokenRef, p.GoogleTokenRef, &out.Source.Google.TokenRef},
		{FieldOneDriveClientID, p.OneDriveClientID, &out.Source.OneDrive.ClientID},
		{FieldOneDriveTokenFile, p.OneDriveTokenFile, &out.Source.OneDrive.TokenPath},
		{FieldOneDriveTokenRef, p.OneDriveTokenRef, &out.Source.OneDrive.TokenRef},
	} {
		if !decided.Has(f.field) && f.val != "" {
			*f.dest = f.val
		}
	}

	// The repeatable flags merge on emptiness rather than on being decided: a
	// caller that passed none has nothing to lose by taking the profile's, and
	// one that passed some means those instead of, not in addition to, them.
	if len(out.Tags) == 0 && len(p.Tags) > 0 {
		out.Tags = append([]string(nil), p.Tags...)
	}
	if len(out.Source.Excludes) == 0 && len(p.Excludes) > 0 {
		out.Source.Excludes = append([]string(nil), p.Excludes...)
	}

	authRef := p.AuthRef
	if decided.Has(FieldAuthRef) && base.AuthRef != "" {
		authRef = base.AuthRef
	}
	out.AuthRef = authRef
	if authRef != "" {
		auth, ok := cfg.Auth[authRef]
		if !ok {
			return Backup{}, fmt.Errorf("profile %q references unknown auth %q", name, authRef)
		}
		if err := applyAuth(&out, decided, auth); err != nil {
			return Backup{}, fmt.Errorf("profile %q auth %q: %w", name, authRef, err)
		}
	}

	return out, nil
}

// ApplyProfileAuth folds a named auth entry's credentials into base, after
// checking the entry's provider against what the source's scheme requires.
//
// The provider rule is the reason this is exported rather than left inside
// MergeProfileBackup: a gdrive source needs a "google" entry and a onedrive
// source a "onedrive" one, and a caller pairing them the other way should get an
// error rather than an unauthenticated run. Anything acting on a profiles file
// needs that check, including code that writes an auth entry before using it.
func ApplyProfileAuth(base Backup, decided FieldSet, auth profile.Auth) (Backup, error) {
	out := base
	if err := applyAuth(&out, decided, auth); err != nil {
		return Backup{}, err
	}
	return out, nil
}

func applyAuth(cfg *Backup, decided FieldSet, auth profile.Auth) error {
	uri, err := ParseSourceURI(cfg.Source.URI)
	if err != nil {
		return fmt.Errorf("parse source URI: %w", err)
	}

	var required string
	switch uri.Scheme {
	case "gdrive", "gdrive-changes":
		required = "google"
	case "onedrive", "onedrive-changes":
		required = "onedrive"
	default:
		return fmt.Errorf("auth refs are only valid for Google Drive and OneDrive sources")
	}
	if auth.Provider != "" && auth.Provider != required {
		return fmt.Errorf("provider mismatch: source requires %q but auth entry is %q", required, auth.Provider)
	}

	var fields []struct {
		field Field
		val   string
		dest  *string
	}
	switch required {
	case "google":
		fields = []struct {
			field Field
			val   string
			dest  *string
		}{
			{FieldGoogleCreds, auth.GoogleCreds, &cfg.Source.Google.CredsPath},
			{FieldGoogleCredsRef, auth.GoogleCredsRef, &cfg.Source.Google.CredsRef},
			{FieldGoogleCredsJSON, auth.GoogleCredsJSON, &cfg.Source.Google.CredsJSON},
			{FieldGoogleTokenFile, auth.GoogleTokenFile, &cfg.Source.Google.TokenPath},
			{FieldGoogleTokenRef, auth.GoogleTokenRef, &cfg.Source.Google.TokenRef},
		}
	case "onedrive":
		fields = []struct {
			field Field
			val   string
			dest  *string
		}{
			{FieldOneDriveClientID, auth.OneDriveClientID, &cfg.Source.OneDrive.ClientID},
			{FieldOneDriveTokenFile, auth.OneDriveTokenFile, &cfg.Source.OneDrive.TokenPath},
			{FieldOneDriveTokenRef, auth.OneDriveTokenRef, &cfg.Source.OneDrive.TokenRef},
		}
	}
	for _, f := range fields {
		if !decided.Has(f.field) && f.val != "" {
			*f.dest = f.val
		}
	}
	return nil
}
