package config_test

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
	"github.com/cloudstic/cli/pkg/secretref"
	secretrefbackends "github.com/cloudstic/cli/pkg/secretref/backends"
)

// These began as characterization tests in cmd/cloudstic, written before the
// resolution logic moved here, and they moved with the code they pin.
//
// env:// is used throughout because it is the one secret backend available on
// every platform and needs no keychain, so these stay hermetic. Importing
// pkg/secretref/backends is a test-only dependency and does not count against
// what this package costs to import — `go list -deps` excludes test imports,
// which is what keeps TestPublicPackagesPullNoVendorSDK honest about it.

func testResolver() *secretref.Resolver { return secretrefbackends.NewDefaultResolver() }

func TestResolveValue_InlineWinsOverSecretRef(t *testing.T) {
	t.Setenv("CLOUDSTIC_TEST_SECRET", "from-secret-ref")

	got, err := config.ResolveValue(context.Background(), testResolver(),
		"s3_access_key", "inline-value", "env://CLOUDSTIC_TEST_SECRET")
	if err != nil {
		t.Fatalf("ResolveValue: %v", err)
	}
	if got != "inline-value" {
		t.Errorf("got %q, want %q: an inline value must win over a secret reference, "+
			"and must do so without resolving the reference at all", got, "inline-value")
	}
}

// TestResolveValue_InlineWinsOverBrokenRef is the sharp edge of the rule
// above: because an inline value short-circuits, a reference that cannot be
// resolved is not an error when an inline value is also present.
func TestResolveValue_InlineWinsOverBrokenRef(t *testing.T) {
	got, err := config.ResolveValue(context.Background(), testResolver(),
		"s3_access_key", "inline-value", "env://CLOUDSTIC_TEST_DEFINITELY_UNSET")
	if err != nil {
		t.Fatalf("a broken reference alongside an inline value must not fail: %v", err)
	}
	if got != "inline-value" {
		t.Errorf("got %q, want %q", got, "inline-value")
	}
}

func TestResolveValue_FallsBackToSecretRef(t *testing.T) {
	t.Setenv("CLOUDSTIC_TEST_SECRET", "from-secret-ref")

	got, err := config.ResolveValue(context.Background(), testResolver(),
		"s3_access_key", "", "env://CLOUDSTIC_TEST_SECRET")
	if err != nil {
		t.Fatalf("ResolveValue: %v", err)
	}
	if got != "from-secret-ref" {
		t.Errorf("got %q, want %q", got, "from-secret-ref")
	}
}

func TestResolveValue_NeitherIsEmptyNotAnError(t *testing.T) {
	got, err := config.ResolveValue(context.Background(), testResolver(), "s3_access_key", "", "")
	if err != nil {
		t.Fatalf("ResolveValue: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestResolveValue_ErrorNamesTheField pins the diagnostic, which is the whole
// reason resolution happens at this stage rather than at connect time: a
// missing secret must say which profile field asked for it and what reference
// failed, not surface later as an empty credential and an auth error from a
// cloud provider.
func TestResolveValue_ErrorNamesTheField(t *testing.T) {
	_, err := config.ResolveValue(context.Background(), testResolver(),
		"b2_app_key", "", "env://CLOUDSTIC_TEST_DEFINITELY_UNSET")
	if err == nil {
		t.Fatal("expected an error for an unresolvable secret reference, got nil")
	}
	for _, want := range []string{"b2_app_key", "env://CLOUDSTIC_TEST_DEFINITELY_UNSET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestResolveValue_RejectsUnknownScheme(t *testing.T) {
	_, err := config.ResolveValue(context.Background(), testResolver(), "password", "", "not-a-ref")
	if err == nil {
		t.Fatal("expected an error for a malformed secret reference, got nil")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error = %q, want it to name the field", err)
	}
}

// TestResolveValue_NilResolverFailsLoudly covers a case a library caller can
// reach that the CLI cannot: passing no resolver at all. Inline values must
// still work, and a reference must be a clear error rather than a silent empty
// credential.
func TestResolveValue_NilResolverFailsLoudly(t *testing.T) {
	got, err := config.ResolveValue(context.Background(), nil, "s3_access_key", "inline", "")
	if err != nil || got != "inline" {
		t.Errorf("inline value with a nil resolver = (%q, %v), want (\"inline\", nil)", got, err)
	}

	_, err = config.ResolveValue(context.Background(), nil, "password", "", "env://ANYTHING")
	if err == nil {
		t.Fatal("a secret reference with no resolver must fail, not yield an empty credential")
	}
	if !strings.Contains(err.Error(), "no secret resolver") {
		t.Errorf("error = %q, want it to say a resolver is missing", err)
	}
}

func TestFromProfileStore_ResolvesLocationAndCredentials(t *testing.T) {
	t.Setenv("CLOUDSTIC_TEST_ACCESS", "AKIA-from-ref")

	got, err := config.FromProfileStore(context.Background(), profile.Store{
		URI:               "s3:bucket/prefix",
		S3Endpoint:        "https://minio.example",
		S3Region:          "eu-west-3",
		S3AccessKeySecret: "env://CLOUDSTIC_TEST_ACCESS",
		S3SecretKey:       "inline-secret",
		KMSKeyARN:         "arn:aws:kms:eu-west-3:1:key/x",
	}, testResolver())
	if err != nil {
		t.Fatalf("FromProfileStore: %v", err)
	}

	for _, tc := range []struct{ name, got, want string }{
		{"URI", got.Store.URI, "s3:bucket/prefix"},
		{"endpoint", got.Store.S3.Endpoint, "https://minio.example"},
		{"region", got.Store.S3.Region, "eu-west-3"},
		{"access key from reference", got.Store.S3.AccessKey, "AKIA-from-ref"},
		{"secret key inline", got.Store.S3.SecretKey, "inline-secret"},
		{"kms arn", got.Unlock.KMS.KeyARN, "arn:aws:kms:eu-west-3:1:key/x"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestFromProfileStore_SilentProfileLeavesZeroValues confirms the result is
// usable as-is: a profile that names only a store URI must not invent
// credentials, and must leave the behaviour fields at their safe defaults.
func TestFromProfileStore_SilentProfileLeavesZeroValues(t *testing.T) {
	got, err := config.FromProfileStore(context.Background(),
		profile.Store{URI: "local:/srv/backups"}, testResolver())
	if err != nil {
		t.Fatalf("FromProfileStore: %v", err)
	}
	if got.Store.S3.AccessKey != "" || got.Unlock.Password != "" {
		t.Error("a profile naming no credentials must not produce any")
	}
	if got.DisablePackfile {
		t.Error("a profile saying nothing about packfiles must leave them enabled")
	}
}

// TestMergeProfileStore_LocationIsTakenOnlyWhenNamed and its credential
// counterpart below pin the asymmetry between the two groups of fields. It is
// deliberate and load-bearing, and the pair exists so that collapsing them
// into one rule fails a test rather than silently changing which store a
// command talks to, or with whose identity.
func TestMergeProfileStore_LocationIsTakenOnlyWhenNamed(t *testing.T) {
	cfg := config.Client{Store: config.Store{
		URI: "local:/from-env",
		S3:  config.S3{Region: "us-east-1"},
	}}

	// A profile that says nothing about location leaves it alone.
	cfg, err := config.MergeProfileStore(context.Background(), cfg, nil, profile.Store{}, testResolver())
	if err != nil {
		t.Fatalf("MergeProfileStore: %v", err)
	}
	if cfg.Store.URI != "local:/from-env" {
		t.Errorf("URI = %q, want the pre-existing value kept when the profile names none", cfg.Store.URI)
	}
	if cfg.Store.S3.Region != "us-east-1" {
		t.Errorf("region = %q, want the pre-existing value kept", cfg.Store.S3.Region)
	}

	// One that does name a location replaces it.
	cfg, err = config.MergeProfileStore(context.Background(), cfg, nil,
		profile.Store{URI: "s3:bucket"}, testResolver())
	if err != nil {
		t.Fatalf("MergeProfileStore: %v", err)
	}
	if cfg.Store.URI != "s3:bucket" {
		t.Errorf("URI = %q, want the profile's value", cfg.Store.URI)
	}
}

// TestMergeProfileStore_CredentialsAreClearedWhenTheProfileIsSilent is the
// other half, and the surprising one. Selecting a profile is an explicit
// choice of which store to talk to, so a credential left over from the
// environment must not follow it there — reaching the profile's store with an
// unrelated identity is worse than failing to reach it.
func TestMergeProfileStore_CredentialsAreClearedWhenTheProfileIsSilent(t *testing.T) {
	cfg := config.Client{Store: config.Store{
		S3: config.S3{AccessKey: "AKIA-from-environment", SecretKey: "secret-from-environment"},
	}}

	cfg, err := config.MergeProfileStore(context.Background(), cfg, nil,
		profile.Store{URI: "s3:other-bucket"}, testResolver())
	if err != nil {
		t.Fatalf("MergeProfileStore: %v", err)
	}
	if cfg.Store.S3.AccessKey != "" || cfg.Store.S3.SecretKey != "" {
		t.Errorf("credentials = (%q, %q), want both cleared: a profile that names none "+
			"must not inherit an ambient identity", cfg.Store.S3.AccessKey, cfg.Store.S3.SecretKey)
	}
}

// TestMergeProfileStore_OverriddenFieldsAreNeverResolved pins the laziness the
// CLI depends on: a field the caller has already decided is not read from the
// profile, so a broken secret reference on a field that is about to be
// replaced is not an error.
func TestMergeProfileStore_OverriddenFieldsAreNeverResolved(t *testing.T) {
	base := config.Client{Unlock: config.Unlock{Password: "from-flag"}}
	decided := config.NewFieldSet(config.FieldPassword)

	cfg, err := config.MergeProfileStore(context.Background(), base, decided, profile.Store{
		PasswordSecret: "env://CLOUDSTIC_TEST_DEFINITELY_UNSET",
	}, testResolver())
	if err != nil {
		t.Fatalf("an overridden field must not be resolved, so its broken reference "+
			"must not fail: %v", err)
	}
	if cfg.Unlock.Password != "from-flag" {
		t.Errorf("password = %q, want the caller's value kept", cfg.Unlock.Password)
	}
}

// TestFromProfileStoreMatchesMergeOnZeroValue keeps the convenience entry
// point and the fold from drifting apart. FromProfileStore is defined as
// MergeProfileStore against an empty configuration; if that ever stops being
// true, a library caller and the CLI would resolve the same profile
// differently.
func TestFromProfileStoreMatchesMergeOnZeroValue(t *testing.T) {
	t.Setenv("CLOUDSTIC_TEST_ACCESS", "AKIA-from-ref")

	s := profile.Store{
		URI:               "s3:bucket",
		S3Region:          "eu-west-3",
		S3AccessKeySecret: "env://CLOUDSTIC_TEST_ACCESS",
		StoreSFTPPassword: "sftp-pw",
		KMSKeyARN:         "arn:x",
	}

	fromHelper, err := config.FromProfileStore(context.Background(), s, testResolver())
	if err != nil {
		t.Fatalf("FromProfileStore: %v", err)
	}

	folded, err := config.MergeProfileStore(context.Background(), config.Client{}, nil, s, testResolver())
	if err != nil {
		t.Fatalf("MergeProfileStore: %v", err)
	}

	if fromHelper != folded {
		t.Errorf("FromProfileStore and MergeProfileStore disagree:\n got  %+v\n want %+v", fromHelper, folded)
	}
}
