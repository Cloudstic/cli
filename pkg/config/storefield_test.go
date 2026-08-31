package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"
)

// TestStoreFieldSpecsCoverEveryProfileStoreField is the completeness check the
// hand-written copies never had: every exported field of profile.Store must be
// addressable through the table, as either a direct value or a reference.
//
// Without it, adding a field to the profiles format and forgetting the table
// leaves it settable in YAML and invisible to `store new`, `store show` and the
// TUI form — which is exactly how the four parallel lists drifted (issue #568).
func TestStoreFieldSpecsCoverEveryProfileStoreField(t *testing.T) {
	reached := map[string]bool{}
	for _, f := range StoreFieldSpecs() {
		var s profile.Store
		f.SetInline(&s, "sentinel")
		f.SetRef(&s, "sentinel")
		v := reflect.ValueOf(s)
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).String() == "sentinel" {
				reached[v.Type().Field(i).Name] = true
			}
		}
	}

	var missing []string
	rt := reflect.TypeOf(profile.Store{})
	for i := 0; i < rt.NumField(); i++ {
		if name := rt.Field(i).Name; !reached[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("profile.Store fields unreachable through StoreFieldSpecs: %v\n"+
			"Add them to fieldSpecs() in field.go, or they will be settable in the "+
			"profiles YAML and invisible to every command that manages a store.", missing)
	}
}

// TestStoreFieldFlagNamesMatchProfileKeys pins the derivation rather than a
// hand-written list: a flag is its YAML key with underscores turned into
// dashes, and a reference flag is that plus "-secret".
func TestStoreFieldFlagNamesMatchProfileKeys(t *testing.T) {
	for _, f := range StoreFieldSpecs() {
		base := strings.ReplaceAll(f.ProfileKey(), "_", "-")
		if got := f.FlagName(); got != "" && got != base {
			t.Errorf("%s: flag %q does not match profile key %q", f.Field(), got, f.ProfileKey())
		}
		if got := f.SecretFlagName(); got != "" && got != base+"-secret" {
			t.Errorf("%s: secret flag %q does not match profile key %q", f.Field(), got, f.ProfileKey())
		}
		if f.FlagName() == "" && f.SecretFlagName() == "" {
			t.Errorf("%s: field is settable by neither a flag nor a secret flag", f.Field())
		}
	}
}

// TestMergeStoreInto_UndecidedFieldsKeepExisting is `store new` against an
// existing entry: change one setting, keep the rest.
func TestMergeStoreInto_UndecidedFieldsKeepExisting(t *testing.T) {
	existing := profile.Store{URI: "s3:old", S3Region: "eu-west-3", S3AccessKeySecret: "env://OLD"}
	incoming := profile.Store{URI: "s3:new"}

	got := MergeStoreInto(existing, incoming, func(flag string) bool { return flag == "uri" })

	if got.URI != "s3:new" {
		t.Errorf("a decided field must come from incoming, got %q", got.URI)
	}
	if got.S3Region != "eu-west-3" {
		t.Errorf("an undecided field must keep the existing value, got %q", got.S3Region)
	}
	if got.S3AccessKeySecret != "env://OLD" {
		t.Errorf("an undecided secret reference must be kept, got %q", got.S3AccessKeySecret)
	}
}

// TestMergeStoreInto_DecidingAValueDoesNotDecideItsReference guards the reason
// decided is keyed by flag name: -s3-access-key and -s3-access-key-secret are
// two separate flags on the same field.
func TestMergeStoreInto_DecidingAValueDoesNotDecideItsReference(t *testing.T) {
	existing := profile.Store{S3AccessKey: "AKIAOLD", S3AccessKeySecret: "env://OLD"}
	incoming := profile.Store{S3AccessKey: "AKIANEW"}

	got := MergeStoreInto(existing, incoming, func(flag string) bool { return flag == "s3-access-key" })

	if got.S3AccessKey != "AKIANEW" {
		t.Errorf("decided inline value must win, got %q", got.S3AccessKey)
	}
	if got.S3AccessKeySecret != "env://OLD" {
		t.Errorf("the reference was not decided and must survive, got %q", got.S3AccessKeySecret)
	}
}
