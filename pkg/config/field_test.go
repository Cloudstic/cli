package config_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
)

func TestFields_AreUniqueAndKeyed(t *testing.T) {
	seenField := map[config.Field]bool{}
	seenKey := map[string]bool{}
	for _, f := range config.StoreFields() {
		if seenField[f] {
			t.Errorf("field %q listed twice", f)
		}
		seenField[f] = true

		key := f.ProfileKey()
		if key == "" {
			t.Errorf("field %q has no profiles-file key, so an error about it "+
				"could not name the entry to go fix", f)
			continue
		}
		if seenKey[key] {
			t.Errorf("profiles-file key %q claimed by two fields", key)
		}
		seenKey[key] = true
	}
	if len(seenField) == 0 {
		t.Fatal("StoreFields returned nothing; every test here would pass vacuously")
	}
}

func TestField_ProfileKeyOfUnknownField(t *testing.T) {
	if got := config.Field("not-a-field").ProfileKey(); got != "" {
		t.Errorf("ProfileKey = %q, want empty for an unrecognized field", got)
	}
}

// TestFieldsCoverEveryProfileStoreField is the regression test the single table
// exists for. It walks profile.Store by reflection, so a field added to the
// profiles format that nothing reads fails here rather than being silently
// ignored — which is how a user's configured credential would go unused with no
// error anywhere.
func TestFieldsCoverEveryProfileStoreField(t *testing.T) {
	const sentinel = "COVERAGE-SENTINEL"
	t.Setenv("CLOUDSTIC_TEST_COVERAGE", sentinel)

	typ := reflect.TypeOf(profile.Store{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if typ.Field(i).Type.Kind() != reflect.String {
			t.Fatalf("profile.Store.%s is not a string; this test assumes every "+
				"store field is one and needs updating", name)
		}

		t.Run(name, func(t *testing.T) {
			var s profile.Store
			// A *Secret field states a reference; every other field states its
			// value directly.
			value := sentinel
			if strings.HasSuffix(name, "Secret") {
				value = "env://CLOUDSTIC_TEST_COVERAGE"
			}
			reflect.ValueOf(&s).Elem().Field(i).SetString(value)

			got, err := config.MergeProfileStore(context.Background(), config.Client{}, nil, s, testResolver())
			if err != nil {
				t.Fatalf("MergeProfileStore: %v", err)
			}
			if !containsString(reflect.ValueOf(got), sentinel) {
				t.Errorf("profile.Store.%s = %q did not reach the resolved configuration; "+
					"no entry in fieldSpecs reads it", name, value)
			}
		})
	}
}

// containsString reports whether any string anywhere in v equals want.
func containsString(v reflect.Value, want string) bool {
	switch v.Kind() {
	case reflect.String:
		return v.String() == want
	case reflect.Struct:
		for i := range v.NumField() {
			if containsString(v.Field(i), want) {
				return true
			}
		}
	default:
	}
	return false
}

func TestFieldSet(t *testing.T) {
	s := config.NewFieldSet(config.FieldS3AccessKey, config.FieldPassword)

	if !s.Has(config.FieldS3AccessKey) || !s.Has(config.FieldPassword) {
		t.Error("NewFieldSet did not record the fields it was given")
	}
	if s.Has(config.FieldS3SecretKey) {
		t.Error("FieldSet reports a field it was never given")
	}

	// A nil set decides nothing, which is what makes it the "no overrides"
	// argument rather than an error.
	var nilSet config.FieldSet
	if nilSet.Has(config.FieldS3AccessKey) {
		t.Error("a nil FieldSet must decide nothing")
	}
}

func TestFieldsSetIn(t *testing.T) {
	cfg := config.Client{
		Store:  config.Store{URI: "s3:bucket", S3: config.S3{AccessKey: "AKIA"}},
		Unlock: config.Unlock{KMS: config.KMS{KeyARN: "arn:x"}},
	}
	got := config.FieldsSetIn(cfg)

	for _, f := range []config.Field{config.FieldStoreURI, config.FieldS3AccessKey, config.FieldKMSKeyARN} {
		if !got.Has(f) {
			t.Errorf("FieldsSetIn omitted %q, which the config sets", f)
		}
	}
	for _, f := range []config.Field{config.FieldS3SecretKey, config.FieldPassword, config.FieldB2KeyID} {
		if got.Has(f) {
			t.Errorf("FieldsSetIn reported %q, which the config leaves empty", f)
		}
	}

	// Fields the config does not set are exactly the ones a profile may still
	// supply, so an empty config must decide nothing at all.
	if len(config.FieldsSetIn(config.Client{})) != 0 {
		t.Error("FieldsSetIn on a zero Client must be empty, or a profile could never supply anything")
	}
}

// TestMergeProfileStore_DecidedFieldsSurviveTheFold exercises the seam a caller
// with its own configuration mechanism actually uses: derive the set from what
// you filled in, and the profile supplies only the rest.
func TestMergeProfileStore_DecidedFieldsSurviveTheFold(t *testing.T) {
	mine := config.Client{Store: config.Store{
		URI: "local:/my-own-choice",
		S3:  config.S3{AccessKey: "mine"},
	}}

	got, err := config.MergeProfileStore(context.Background(), mine, config.FieldsSetIn(mine),
		profile.Store{
			URI:         "s3:the-profiles-bucket",
			S3AccessKey: "the-profiles-key",
			S3SecretKey: "the-profiles-secret",
			S3Region:    "eu-west-3",
		}, testResolver())
	if err != nil {
		t.Fatalf("MergeProfileStore: %v", err)
	}

	if got.Store.URI != "local:/my-own-choice" {
		t.Errorf("URI = %q, want the caller's decision kept", got.Store.URI)
	}
	if got.Store.S3.AccessKey != "mine" {
		t.Errorf("access key = %q, want the caller's decision kept", got.Store.S3.AccessKey)
	}
	// Everything the caller did not decide still comes from the profile, which
	// is the whole point of layering rather than choosing one or the other.
	if got.Store.S3.SecretKey != "the-profiles-secret" {
		t.Errorf("secret key = %q, want the profile's value", got.Store.S3.SecretKey)
	}
	if got.Store.S3.Region != "eu-west-3" {
		t.Errorf("region = %q, want the profile's value", got.Store.S3.Region)
	}
}

// The merge returns a new value, so a caller can resolve the same profile
// against several layers without the first fold having disturbed its input.
func TestMergeProfileStore_DoesNotMutateBase(t *testing.T) {
	base := config.Client{Store: config.Store{URI: "local:/base"}}

	if _, err := config.MergeProfileStore(context.Background(), base, nil,
		profile.Store{URI: "s3:bucket", S3AccessKey: "k"}, testResolver()); err != nil {
		t.Fatalf("MergeProfileStore: %v", err)
	}

	if base.Store.URI != "local:/base" || base.Store.S3.AccessKey != "" {
		t.Errorf("base was mutated: %+v", base.Store)
	}
}
