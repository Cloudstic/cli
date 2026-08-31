package config

import (
	"strings"

	"github.com/cloudstic/cli/pkg/profile"
)

// StoreField describes one field of a profiles-file store entry: what it is
// called on the command line and in the YAML, whether it may be given as a
// secret reference, and how to read and write it on an entry.
//
// It exists so that a program managing a profiles file — the cloudstic CLI's
// `store new` and `store show`, its terminal UI, or anything else — derives its
// input surface from the same table this package merges with, instead of
// restating the field list. Restating it is what this replaces: the CLI held
// four separate hand-written copies of these 22 fields, two of them keyed by
// bare flag strings with nothing checking they stayed complete, so a field
// added to the profiles format was covered for `-profile` and silently not for
// `store new` (issue #568).
//
// The type is opaque on purpose. The metadata is reached through methods so
// that a column can be added here without changing a struct literal in every
// caller, which is the failure this whole change is about.
type StoreField struct{ spec fieldSpec }

// StoreFieldSpecs returns every field of a store entry, in a fresh slice.
//
// Iterate it to build a flag set, a form, or a rendered table. See StoreFields
// for the plainer list of Field values used to build a FieldSet.
func StoreFieldSpecs() []StoreField {
	specs := fieldSpecs()
	out := make([]StoreField, 0, len(specs))
	for _, s := range specs {
		out = append(out, StoreField{spec: s})
	}
	return out
}

// Field returns the Field constant naming this setting, whose string value is
// the global cloudstic flag that carries it.
func (f StoreField) Field() Field { return f.spec.field }

// ProfileKey returns the profiles-file (YAML) key for this field.
func (f StoreField) ProfileKey() string { return f.spec.key }

// FlagName returns the flag a store-management command should accept for this
// field's direct value, derived from the YAML key rather than stated
// separately — `s3_access_key` yields `s3-access-key`. Empty when the field has
// no inline form, which is the case for the repository credentials: a profiles
// file may say where the password lives, never what it is.
func (f StoreField) FlagName() string {
	if f.spec.storeInline == nil {
		return ""
	}
	return strings.ReplaceAll(f.spec.key, "_", "-")
}

// SecretFlagName returns the flag for this field's scheme://path reference, or
// empty when the field has no reference form.
func (f StoreField) SecretFlagName() string {
	if f.spec.storeRef == nil {
		return ""
	}
	return strings.ReplaceAll(f.spec.key, "_", "-") + "-secret"
}

// Label returns the human-readable name for this field. The reference row of a
// rendered table appends " Secret" rather than carrying a second label.
func (f StoreField) Label() string { return f.spec.label }

// Sensitive reports whether the field's direct value is a credential, and so
// must never be rendered back to a user. The reference is always safe to show:
// it names a location, not a secret.
func (f StoreField) Sensitive() bool { return f.spec.sensitive }

// Get returns the field's direct value and secret reference on s. Either may be
// empty, including because the field has no such form.
func (f StoreField) Get(s profile.Store) (inline, ref string) { return f.spec.read(s) }

// SetInline writes the field's direct value on s. It is a no-op for a field
// with no inline form, so a caller may loop over every field without checking.
func (f StoreField) SetInline(s *profile.Store, v string) {
	if f.spec.storeInline != nil {
		*f.spec.storeInline(s) = v
	}
}

// SetRef writes the field's secret reference on s. It is a no-op for a field
// with no reference form.
func (f StoreField) SetRef(s *profile.Store, v string) {
	if f.spec.storeRef != nil {
		*f.spec.storeRef(s) = v
	}
}

// MergeStoreInto folds incoming over existing and returns the result: a field
// the caller decided is taken from incoming, and every other field keeps
// whatever existing held.
//
// This is the write-direction counterpart to MergeProfileStore, which resolves
// a profile entry into a Client for *reading* a repository. This one edits the
// entry itself, and is what lets `cloudstic store new <name>` against an
// existing store change one setting without restating the rest.
//
// decided is asked about flag names rather than Fields because a field's direct
// value and its secret reference are separately settable — `-s3-access-key` and
// `-s3-access-key-secret` are two flags, and deciding one must not silently
// decide the other. Pass the names from FlagName and SecretFlagName; a nil
// decided decides nothing, making this "keep everything existing had".
//
// An undecided field falls back to existing only when existing holds something.
// That keeps the merge total: a field neither side has stays empty rather than
// becoming a zero value that later reads as "deliberately cleared".
func MergeStoreInto(existing, incoming profile.Store, decided func(flagName string) bool) profile.Store {
	if decided == nil {
		decided = func(string) bool { return false }
	}
	out := incoming
	for _, f := range StoreFieldSpecs() {
		existingInline, existingRef := f.Get(existing)
		if name := f.FlagName(); name != "" && !decided(name) && existingInline != "" {
			f.SetInline(&out, existingInline)
		}
		if name := f.SecretFlagName(); name != "" && !decided(name) && existingRef != "" {
			f.SetRef(&out, existingRef)
		}
	}
	return out
}
