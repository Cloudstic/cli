package config

import "github.com/cloudstic/cli/pkg/profile"

// Field names one setting a profile can supply, for callers that layer their
// own configuration mechanism over a profile.
//
// It exists because "which fields have you already decided?" cannot be
// expressed by a Client alone: a plain string field cannot distinguish "unset"
// from "deliberately empty", and the difference decides whether a profile's
// value applies. Naming each field as a constant makes that question answerable
// without a bare string, which silently meant "not decided" whenever it was
// misspelled — and there were two plausible spellings of every field, since the
// profiles file writes s3_access_key where the cloudstic flag is -s3-access-key.
//
// A Field's string value is the cloudstic flag that carries it, so a program
// layering flags of its own can use the flag name directly; ProfileKey gives
// the profiles-file spelling of the same field.
type Field string

// The fields a profile can supply. MergeProfileStore arbitrates between these
// and a caller's own values; Fields returns the complete set.
const (
	FieldStoreURI          Field = "store"
	FieldS3Endpoint        Field = "s3-endpoint"
	FieldS3Region          Field = "s3-region"
	FieldS3Profile         Field = "s3-profile"
	FieldS3AccessKey       Field = "s3-access-key"
	FieldS3SecretKey       Field = "s3-secret-key"
	FieldB2KeyID           Field = "b2-key-id"
	FieldB2AppKey          Field = "b2-app-key"
	FieldStoreSFTPPassword Field = "store-sftp-password"
	FieldStoreSFTPKey      Field = "store-sftp-key"
	FieldPassword          Field = "password"
	FieldEncryptionKey     Field = "encryption-key"
	FieldRecoveryKey       Field = "recovery-key"
	FieldKMSKeyARN         Field = "kms-key-arn"
	FieldKMSRegion         Field = "kms-region"
	FieldKMSEndpoint       Field = "kms-endpoint"
)

// fieldKind distinguishes the two merge behaviours, which are not
// interchangeable — see MergeProfileStore for why the difference is
// load-bearing.
type fieldKind int

const (
	// kindLocation is taken only when the profile names a value, so a silent
	// profile leaves whatever the caller had.
	kindLocation fieldKind = iota + 1
	// kindCredential is taken whenever the caller has not decided it, empty
	// included, so selecting a profile clears an ambient credential.
	kindCredential
)

// fieldSpec is everything the merge needs to know about one field: how it
// behaves, where the profile states it, where it lands, and what to call it in
// an error.
type fieldSpec struct {
	field Field
	// key is the profiles-file spelling, used in errors so a failure says
	// which YAML entry to go fix rather than which flag would have replaced it.
	key  string
	kind fieldKind
	// inline and ref read the profile's direct value and its secret reference.
	// Location fields have no reference and leave ref nil.
	inline func(profile.Store) string
	ref    func(profile.Store) string
	dest   func(*Client) *string
}

// fieldSpecs is the single table of which profile fields exist, how each one
// merges, and where it lands. Every other function here reads it, so the merge,
// the enumeration and the "is this set?" check cannot disagree about the set of
// fields — which two parallel lists previously could.
func fieldSpecs() []fieldSpec {
	return []fieldSpec{
		{FieldStoreURI, "uri", kindLocation,
			func(s profile.Store) string { return s.URI }, nil,
			func(c *Client) *string { return &c.Store.URI }},
		{FieldS3Endpoint, "s3_endpoint", kindLocation,
			func(s profile.Store) string { return s.S3Endpoint }, nil,
			func(c *Client) *string { return &c.Store.S3.Endpoint }},
		{FieldS3Region, "s3_region", kindLocation,
			func(s profile.Store) string { return s.S3Region }, nil,
			func(c *Client) *string { return &c.Store.S3.Region }},
		{FieldKMSKeyARN, "kms_key_arn", kindLocation,
			func(s profile.Store) string { return s.KMSKeyARN }, nil,
			func(c *Client) *string { return &c.Unlock.KMS.KeyARN }},
		{FieldKMSRegion, "kms_region", kindLocation,
			func(s profile.Store) string { return s.KMSRegion }, nil,
			func(c *Client) *string { return &c.Unlock.KMS.Region }},
		{FieldKMSEndpoint, "kms_endpoint", kindLocation,
			func(s profile.Store) string { return s.KMSEndpoint }, nil,
			func(c *Client) *string { return &c.Unlock.KMS.Endpoint }},

		{FieldS3Profile, "s3_profile", kindCredential,
			func(s profile.Store) string { return s.S3Profile }, nil,
			func(c *Client) *string { return &c.Store.S3.Profile }},
		{FieldS3AccessKey, "s3_access_key", kindCredential,
			func(s profile.Store) string { return s.S3AccessKey },
			func(s profile.Store) string { return s.S3AccessKeySecret },
			func(c *Client) *string { return &c.Store.S3.AccessKey }},
		{FieldS3SecretKey, "s3_secret_key", kindCredential,
			func(s profile.Store) string { return s.S3SecretKey },
			func(s profile.Store) string { return s.S3SecretKeySecret },
			func(c *Client) *string { return &c.Store.S3.SecretKey }},
		{FieldB2KeyID, "b2_key_id", kindCredential,
			func(s profile.Store) string { return s.B2KeyID },
			func(s profile.Store) string { return s.B2KeyIDSecret },
			func(c *Client) *string { return &c.Store.B2.KeyID }},
		{FieldB2AppKey, "b2_app_key", kindCredential,
			func(s profile.Store) string { return s.B2AppKey },
			func(s profile.Store) string { return s.B2AppKeySecret },
			func(c *Client) *string { return &c.Store.B2.AppKey }},
		{FieldStoreSFTPPassword, "store_sftp_password", kindCredential,
			func(s profile.Store) string { return s.StoreSFTPPassword },
			func(s profile.Store) string { return s.StoreSFTPPasswordSecret },
			func(c *Client) *string { return &c.Store.SFTP.Password }},
		{FieldStoreSFTPKey, "store_sftp_key", kindCredential,
			func(s profile.Store) string { return s.StoreSFTPKey },
			func(s profile.Store) string { return s.StoreSFTPKeySecret },
			func(c *Client) *string { return &c.Store.SFTP.Key }},
		// The repository credentials are reference-only: a profiles file may
		// say where the password lives, never what it is.
		{FieldPassword, "password", kindCredential,
			nil, func(s profile.Store) string { return s.PasswordSecret },
			func(c *Client) *string { return &c.Unlock.Password }},
		{FieldEncryptionKey, "encryption_key", kindCredential,
			nil, func(s profile.Store) string { return s.EncryptionKeySecret },
			func(c *Client) *string { return &c.Unlock.EncryptionKey }},
		{FieldRecoveryKey, "recovery_key", kindCredential,
			nil, func(s profile.Store) string { return s.RecoveryKeySecret },
			func(c *Client) *string { return &c.Unlock.RecoveryKey }},
	}
}

// read returns the profile's inline value and secret reference for this field.
func (f fieldSpec) read(s profile.Store) (inline, ref string) {
	if f.inline != nil {
		inline = f.inline(s)
	}
	if f.ref != nil {
		ref = f.ref(s)
	}
	return inline, ref
}

// ProfileKey returns the profiles-file key that states this field, which is not
// always the field's own string: the file writes s3_access_key where the
// cloudstic flag is -s3-access-key. It is the spelling to show a user who has
// to go edit their profiles file.
//
// An unrecognized Field returns the empty string.
func (f Field) ProfileKey() string {
	for _, spec := range fieldSpecs() {
		if spec.field == f {
			return spec.key
		}
	}
	return backupFieldKeys[f]
}

// StoreFields returns every store-related field a profile can supply, in a
// fresh slice. See BackupFields for the ones that describe what to back up.
//
// Iterate it to build a FieldSet from whatever "the user gave me this" means in
// your own configuration mechanism — a set of parsed flags, keys present in a
// TOML file, non-empty form inputs. Doing so keeps the set complete as fields
// are added, where a hand-written list would quietly fall behind.
func StoreFields() []Field {
	specs := fieldSpecs()
	out := make([]Field, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.field)
	}
	return out
}

// FieldSet is a set of fields the caller has already decided, and which a
// profile therefore must not supply.
type FieldSet map[Field]struct{}

// NewFieldSet collects fields into a set. It is valid to pass none.
func NewFieldSet(fields ...Field) FieldSet {
	s := make(FieldSet, len(fields))
	for _, f := range fields {
		s[f] = struct{}{}
	}
	return s
}

// Has reports whether f is in the set. The zero FieldSet is empty, so a nil
// set means "nothing decided" rather than being an error.
func (s FieldSet) Has(f Field) bool {
	_, ok := s[f]
	return ok
}

// FieldsSetIn reports which fields cfg holds a non-empty value for.
//
// This is the FieldSet to pass when your own configuration mechanism has no
// notion of "present but empty" — reading a struct you filled in is then exactly
// as good as tracking which keys you filled, and cannot drift from it.
//
// Use an explicit NewFieldSet instead when empty means something: the cloudstic
// CLI does, because `-password ""` is a deliberate choice of no password and
// must still beat the profile's.
func FieldsSetIn(cfg Client) FieldSet {
	s := FieldSet{}
	for _, spec := range fieldSpecs() {
		if *spec.dest(&cfg) != "" {
			s[spec.field] = struct{}{}
		}
	}
	return s
}
