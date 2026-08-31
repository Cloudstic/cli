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

// fieldSpec is everything anyone needs to know about one store field: how it
// merges, where the profile states it, where it lands when resolved, how to
// read and write it on a profile entry, and what to call it to a human.
//
// It is one table rather than several because the alternative was measured:
// the same 22 fields were written out six times across this module — the
// resolved-config merge here, plus `store new`'s flags, its prefill, its
// profile.Store assembly, the `store show` rows, and the TUI form — and the
// four in cmd/cloudstic were stringly-keyed with nothing checking they stayed
// complete (issue #568).
type fieldSpec struct {
	field Field
	// key is the profiles-file spelling, used in errors so a failure says
	// which YAML entry to go fix rather than which flag would have replaced it.
	// It is also what both flag names are derived from, so a field cannot be
	// spelled one way in YAML and another on the command line by accident.
	key  string
	kind fieldKind
	dest func(*Client) *string

	// storeInline and storeRef address the field on a profile entry: the
	// direct value and the scheme://path reference that may stand in for it.
	// A field with no inline form leaves storeInline nil — a profiles file may
	// say where the repository password lives, never what it is — and a field
	// with no reference form leaves storeRef nil.
	storeInline func(*profile.Store) *string
	storeRef    func(*profile.Store) *string

	// label is the human-readable name, used by `store show`. The reference
	// row appends " Secret" rather than carrying a second label.
	label string
	// sensitive marks a field whose inline value must never be rendered back
	// to a user. Only the reference is safe to display.
	sensitive bool
}

// inline and ref read the profile's direct value and its secret reference,
// derived from the addressing functions so there is no second accessor to keep
// in step with them.
func (f fieldSpec) inlineOf(s profile.Store) string {
	if f.storeInline == nil {
		return ""
	}
	return *f.storeInline(&s)
}

func (f fieldSpec) refOf(s profile.Store) string {
	if f.storeRef == nil {
		return ""
	}
	return *f.storeRef(&s)
}

// fieldSpecs is the single table of which profile fields exist, how each one
// merges, and where it lands. Every other function here reads it, so the merge,
// the enumeration and the "is this set?" check cannot disagree about the set of
// fields — which two parallel lists previously could.
func fieldSpecs() []fieldSpec {
	return []fieldSpec{
		{
			field: FieldStoreURI, key: "uri", kind: kindLocation, label: "URI",
			dest:        func(c *Client) *string { return &c.Store.URI },
			storeInline: func(s *profile.Store) *string { return &s.URI },
		},
		{
			field: FieldS3Endpoint, key: "s3_endpoint", kind: kindLocation, label: "S3 Endpoint",
			dest:        func(c *Client) *string { return &c.Store.S3.Endpoint },
			storeInline: func(s *profile.Store) *string { return &s.S3Endpoint },
		},
		{
			field: FieldS3Region, key: "s3_region", kind: kindLocation, label: "S3 Region",
			dest:        func(c *Client) *string { return &c.Store.S3.Region },
			storeInline: func(s *profile.Store) *string { return &s.S3Region },
		},
		{
			field: FieldKMSKeyARN, key: "kms_key_arn", kind: kindLocation, label: "KMS Key ARN",
			dest:        func(c *Client) *string { return &c.Unlock.KMS.KeyARN },
			storeInline: func(s *profile.Store) *string { return &s.KMSKeyARN },
		},
		{
			field: FieldKMSRegion, key: "kms_region", kind: kindLocation, label: "KMS Region",
			dest:        func(c *Client) *string { return &c.Unlock.KMS.Region },
			storeInline: func(s *profile.Store) *string { return &s.KMSRegion },
		},
		{
			field: FieldKMSEndpoint, key: "kms_endpoint", kind: kindLocation, label: "KMS Endpoint",
			dest:        func(c *Client) *string { return &c.Unlock.KMS.Endpoint },
			storeInline: func(s *profile.Store) *string { return &s.KMSEndpoint },
		},

		{
			field: FieldS3Profile, key: "s3_profile", kind: kindCredential, label: "S3 Profile",
			dest:        func(c *Client) *string { return &c.Store.S3.Profile },
			storeInline: func(s *profile.Store) *string { return &s.S3Profile },
		},
		{
			field: FieldS3AccessKey, key: "s3_access_key", kind: kindCredential, label: "S3 Access Key",
			sensitive:   true,
			dest:        func(c *Client) *string { return &c.Store.S3.AccessKey },
			storeInline: func(s *profile.Store) *string { return &s.S3AccessKey },
			storeRef:    func(s *profile.Store) *string { return &s.S3AccessKeySecret },
		},
		{
			field: FieldS3SecretKey, key: "s3_secret_key", kind: kindCredential, label: "S3 Secret Key",
			sensitive:   true,
			dest:        func(c *Client) *string { return &c.Store.S3.SecretKey },
			storeInline: func(s *profile.Store) *string { return &s.S3SecretKey },
			storeRef:    func(s *profile.Store) *string { return &s.S3SecretKeySecret },
		},
		{
			field: FieldB2KeyID, key: "b2_key_id", kind: kindCredential, label: "B2 Key ID",
			sensitive:   true,
			dest:        func(c *Client) *string { return &c.Store.B2.KeyID },
			storeInline: func(s *profile.Store) *string { return &s.B2KeyID },
			storeRef:    func(s *profile.Store) *string { return &s.B2KeyIDSecret },
		},
		{
			field: FieldB2AppKey, key: "b2_app_key", kind: kindCredential, label: "B2 App Key",
			sensitive:   true,
			dest:        func(c *Client) *string { return &c.Store.B2.AppKey },
			storeInline: func(s *profile.Store) *string { return &s.B2AppKey },
			storeRef:    func(s *profile.Store) *string { return &s.B2AppKeySecret },
		},
		{
			field: FieldStoreSFTPPassword, key: "store_sftp_password", kind: kindCredential, label: "SFTP Password",
			sensitive:   true,
			dest:        func(c *Client) *string { return &c.Store.SFTP.Password },
			storeInline: func(s *profile.Store) *string { return &s.StoreSFTPPassword },
			storeRef:    func(s *profile.Store) *string { return &s.StoreSFTPPasswordSecret },
		},
		{
			field: FieldStoreSFTPKey, key: "store_sftp_key", kind: kindCredential, label: "SFTP Key",
			dest:        func(c *Client) *string { return &c.Store.SFTP.Key },
			storeInline: func(s *profile.Store) *string { return &s.StoreSFTPKey },
			storeRef:    func(s *profile.Store) *string { return &s.StoreSFTPKeySecret },
		},

		// The repository credentials are reference-only: a profiles file may
		// say where the password lives, never what it is. They carry no scheme
		// filter because they are a property of the repository rather than of
		// the backend it sits on.
		{
			field: FieldPassword, key: "password", kind: kindCredential, label: "Password", sensitive: true,
			dest:     func(c *Client) *string { return &c.Unlock.Password },
			storeRef: func(s *profile.Store) *string { return &s.PasswordSecret },
		},
		{
			field: FieldEncryptionKey, key: "encryption_key", kind: kindCredential, label: "Encryption Key", sensitive: true,
			dest:     func(c *Client) *string { return &c.Unlock.EncryptionKey },
			storeRef: func(s *profile.Store) *string { return &s.EncryptionKeySecret },
		},
		{
			field: FieldRecoveryKey, key: "recovery_key", kind: kindCredential, label: "Recovery Key", sensitive: true,
			dest:     func(c *Client) *string { return &c.Unlock.RecoveryKey },
			storeRef: func(s *profile.Store) *string { return &s.RecoveryKeySecret },
		},
	}
}

// read returns the profile's inline value and secret reference for this field.
func (f fieldSpec) read(s profile.Store) (inline, ref string) {
	return f.inlineOf(s), f.refOf(s)
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
