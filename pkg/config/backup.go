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
	//
	// A profiles file can state the same cloud credential twice: profile.Profile
	// carries GoogleCreds, GoogleTokenFile and the rest, and so does the
	// profile.Auth entry a profile points at. **The auth entry wins**, field by
	// field — an auth entry silent about a field leaves the profile's value
	// alone rather than blanking it.
	//
	// That order is deliberate and pinned by
	// TestMergeProfileBackup_AuthEntryBeatsProfileCredentials: an auth entry is
	// the shared, deliberate statement of where a provider's credentials live,
	// while the fields on a profile predate auth entries and are kept only so
	// that files written before them keep working. Prefer the auth entry when
	// writing a profiles file; the duplication is a compatibility artefact, not
	// a feature.
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

// backupFieldSpec describes one field a profile can supply about what to back
// up: where the profiles file states it, where it lands in a Backup, and — for
// the cloud credentials — which provider's auth entry may also supply it.
//
// These fields are not all strings, which is why this is a second table rather
// than more rows in fieldSpecs: two bools and two string slices need different
// accessors and merge on different rules. Exactly one accessor group is
// non-nil, and kindOf reports which.
//
// One table rather than three parallel lists. The const block, a key map and a
// hand-written BackupFields() used to state the same 16 fields separately, and
// only the first two were consulted by anything — a field added to two of the
// three silently stopped being covered by the CLI's decided-field set (#569).
type backupFieldSpec struct {
	field Field
	// key is the profiles-file spelling, used in errors so a failure names the
	// YAML entry to go fix rather than the flag that would have replaced it.
	key string

	// str, bl and sl address the field by type. Exactly one is set.
	str struct {
		dest        func(*Backup) *string
		fromProfile func(profile.Profile) string
		// fromAuth reads the field from an auth entry, for the cloud
		// credentials an auth entry may supply instead of the profile.
		// provider names which auth provider owns it.
		fromAuth func(profile.Auth) string
		provider string
	}
	bl struct {
		dest        func(*Backup) *bool
		fromProfile func(profile.Profile) bool
	}
	sl struct {
		dest        func(*Backup) *[]string
		fromProfile func(profile.Profile) []string
	}

	// always takes the profile's value even when it is *empty*, rather than
	// only when the profile names one. The source is the only field like this:
	// the caller asked for this profile, so its source is the source, and an
	// empty result is reported rather than falling back to whatever base held —
	// which would back up the wrong tree under a profile's name.
	//
	// It does NOT mean "even when the caller decided one". A decided field is
	// skipped before this is consulted, because an explicit -source must beat
	// the profile's; that is the precedence the whole FieldSet mechanism
	// exists to enforce. Exempting always from the decided check was proposed
	// in review of #578 and is a regression —
	// TestMergeProfileBackup_DecidedSourceBeatsTheProfile fails under it.
	always bool
}

func backupFieldSpecs() []backupFieldSpec {
	str := func(f Field, key string, dest func(*Backup) *string, from func(profile.Profile) string) backupFieldSpec {
		var s backupFieldSpec
		s.field, s.key = f, key
		s.str.dest, s.str.fromProfile = dest, from
		return s
	}
	cloud := func(f Field, key, provider string, dest func(*Backup) *string,
		from func(profile.Profile) string, fromAuth func(profile.Auth) string) backupFieldSpec {
		s := str(f, key, dest, from)
		s.str.fromAuth, s.str.provider = fromAuth, provider
		return s
	}
	boolean := func(f Field, key string, dest func(*Backup) *bool, from func(profile.Profile) bool) backupFieldSpec {
		var s backupFieldSpec
		s.field, s.key = f, key
		s.bl.dest, s.bl.fromProfile = dest, from
		return s
	}
	slice := func(f Field, key string, dest func(*Backup) *[]string, from func(profile.Profile) []string) backupFieldSpec {
		var s backupFieldSpec
		s.field, s.key = f, key
		s.sl.dest, s.sl.fromProfile = dest, from
		return s
	}

	source := str(FieldSourceURI, "source",
		func(b *Backup) *string { return &b.Source.URI },
		func(p profile.Profile) string { return p.Source })
	source.always = true

	return []backupFieldSpec{
		source,
		str(FieldAuthRef, "auth_ref",
			func(b *Backup) *string { return &b.AuthRef },
			func(p profile.Profile) string { return p.AuthRef }),
		str(FieldExcludeFile, "exclude_file",
			func(b *Backup) *string { return &b.Source.ExcludeFile },
			func(p profile.Profile) string { return p.ExcludeFile }),
		str(FieldVolumeUUID, "volume_uuid",
			func(b *Backup) *string { return &b.Source.VolumeUUID },
			func(p profile.Profile) string { return p.VolumeUUID }),

		cloud(FieldGoogleCreds, "google_credentials", ProviderGoogle,
			func(b *Backup) *string { return &b.Source.Google.CredsPath },
			func(p profile.Profile) string { return p.GoogleCreds },
			func(a profile.Auth) string { return a.GoogleCreds }),
		cloud(FieldGoogleCredsRef, "google_credentials_ref", ProviderGoogle,
			func(b *Backup) *string { return &b.Source.Google.CredsRef },
			func(p profile.Profile) string { return p.GoogleCredsRef },
			func(a profile.Auth) string { return a.GoogleCredsRef }),
		cloud(FieldGoogleCredsJSON, "google_credentials_json", ProviderGoogle,
			func(b *Backup) *string { return &b.Source.Google.CredsJSON },
			func(p profile.Profile) string { return p.GoogleCredsJSON },
			func(a profile.Auth) string { return a.GoogleCredsJSON }),
		cloud(FieldGoogleTokenFile, "google_token_file", ProviderGoogle,
			func(b *Backup) *string { return &b.Source.Google.TokenPath },
			func(p profile.Profile) string { return p.GoogleTokenFile },
			func(a profile.Auth) string { return a.GoogleTokenFile }),
		cloud(FieldGoogleTokenRef, "google_token_ref", ProviderGoogle,
			func(b *Backup) *string { return &b.Source.Google.TokenRef },
			func(p profile.Profile) string { return p.GoogleTokenRef },
			func(a profile.Auth) string { return a.GoogleTokenRef }),
		cloud(FieldOneDriveClientID, "onedrive_client_id", ProviderOneDrive,
			func(b *Backup) *string { return &b.Source.OneDrive.ClientID },
			func(p profile.Profile) string { return p.OneDriveClientID },
			func(a profile.Auth) string { return a.OneDriveClientID }),
		cloud(FieldOneDriveTokenFile, "onedrive_token_file", ProviderOneDrive,
			func(b *Backup) *string { return &b.Source.OneDrive.TokenPath },
			func(p profile.Profile) string { return p.OneDriveTokenFile },
			func(a profile.Auth) string { return a.OneDriveTokenFile }),
		cloud(FieldOneDriveTokenRef, "onedrive_token_ref", ProviderOneDrive,
			func(b *Backup) *string { return &b.Source.OneDrive.TokenRef },
			func(p profile.Profile) string { return p.OneDriveTokenRef },
			func(a profile.Auth) string { return a.OneDriveTokenRef }),

		boolean(FieldIgnoreEmpty, "ignore_empty",
			func(b *Backup) *bool { return &b.IgnoreEmpty },
			func(p profile.Profile) bool { return p.IgnoreEmpty }),
		boolean(FieldSkipNativeFiles, "skip_native_files",
			func(b *Backup) *bool { return &b.Source.SkipNativeFiles },
			func(p profile.Profile) bool { return p.SkipNativeFiles }),

		slice(FieldTags, "tags",
			func(b *Backup) *[]string { return &b.Tags },
			func(p profile.Profile) []string { return p.Tags }),
		slice(FieldExcludes, "excludes",
			func(b *Backup) *[]string { return &b.Source.Excludes },
			func(p profile.Profile) []string { return p.Excludes }),
	}
}

// backupFieldKeys maps each backup field to its profiles-file spelling, derived
// from the table so the two cannot disagree.
var backupFieldKeys = func() map[Field]string {
	m := make(map[Field]string)
	for _, s := range backupFieldSpecs() {
		m[s.field] = s.key
	}
	return m
}()

// BackupFields returns every field a profile can supply about what to back up,
// in a fresh slice. See StoreFields for the ones describing where it goes.
//
// Derived from the table rather than listed, so a field added to the const
// block and the table is covered here without a third edit — the hand-written
// version of this list is what allowed the CLI's decided-field set to fall
// behind.
func BackupFields() []Field {
	specs := backupFieldSpecs()
	out := make([]Field, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.field)
	}
	return out
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
	for _, s := range backupFieldSpecs() {
		// AuthRef has its own rule; see below.
		if s.field == FieldAuthRef {
			continue
		}
		// The repeatable fields merge on emptiness rather than on being
		// decided: a caller that passed none has nothing to lose by taking the
		// profile's, and one that passed some means those instead of, not in
		// addition to, them. So they are not skipped here.
		if s.sl.dest == nil && decided.Has(s.field) {
			continue
		}
		s.applyProfile(&out, p)
	}
	if out.Source.URI == "" {
		return Backup{}, fmt.Errorf("profile %q has empty source", name)
	}

	// AuthRef is the one field where a decided caller value does not simply
	// win: an empty one falls back to the profile's entry, because a cloud
	// source with no auth cannot run at all, so "" reads as "no override"
	// rather than as "no auth entry".
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

// applyProfile folds this field's profile value into out, by the rule its type
// implies: a string is taken when the profile names one (or always, for the
// source), a bool is taken as it stands, and a slice is taken only when the
// destination has none.
func (s backupFieldSpec) applyProfile(out *Backup, p profile.Profile) {
	switch {
	case s.str.dest != nil:
		if v := s.str.fromProfile(p); s.always || v != "" {
			*s.str.dest(out) = v
		}
	case s.bl.dest != nil:
		*s.bl.dest(out) = s.bl.fromProfile(p)
	case s.sl.dest != nil:
		dest := s.sl.dest(out)
		if v := s.sl.fromProfile(p); len(*dest) == 0 && len(v) > 0 {
			*dest = append([]string(nil), v...)
		}
	}
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
	if _, err := ParseSourceURI(cfg.Source.URI); err != nil {
		return fmt.Errorf("parse source URI: %w", err)
	}
	required := ProviderForSourceURI(cfg.Source.URI)
	if required == "" {
		return fmt.Errorf("auth refs are only valid for Google Drive and OneDrive sources")
	}
	if !AuthProviderMatches(required, auth) {
		return fmt.Errorf("provider mismatch: source requires %q but auth entry is %q", required, auth.Provider)
	}

	// One fold over the table, filtered by provider. This used to be three
	// copies of the same loop — one here per provider, plus a third in
	// MergeProfileBackup — over three hand-written field lists.
	//
	// Only the credentials belonging to auth's provider are carried over, so an
	// entry holding both providers' fields (which a hand-edited file may) does
	// not produce a source that would try both.
	for _, s := range backupFieldSpecs() {
		if s.str.fromAuth == nil || s.str.provider != required || decided.Has(s.field) {
			continue
		}
		if v := s.str.fromAuth(auth); v != "" {
			*s.str.dest(cfg) = v
		}
	}
	return nil
}
