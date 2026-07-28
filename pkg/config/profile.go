package config

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/pkg/profile"
	"github.com/cloudstic/cli/pkg/secretref"
)

// The resolver is a parameter rather than a package default on purpose.
//
// Taking one means this package never imports pkg/secretref/backends, which
// links the platform-native keychain implementations — so resolving a profile
// stays cheap, and a caller can supply a resolver with extra schemes
// registered (see backends.Default) rather than being limited to the built-in
// set. It also makes resolution testable without touching a real keychain.
//
// A nil resolver is allowed and means "no secret references": inline values
// still resolve, and any field that names a reference fails with a clear
// error rather than silently yielding an empty credential.

// ResolveValue returns the effective value of a profile field that may be
// given either inline or as a scheme://path secret reference.
//
// An inline value wins and short-circuits: the reference is not consulted, and
// a broken reference alongside an inline value is not an error. When neither is
// set the result is the empty string, which callers read as "the profile says
// nothing about this field".
//
// field names the profiles-file key and appears in the error, so a failure
// says which entry to go fix rather than only which backend refused.
func ResolveValue(ctx context.Context, r *secretref.Resolver, field, inline, ref string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if ref == "" {
		return "", nil
	}
	if r == nil {
		return "", fmt.Errorf("resolve profile store field %q from %q: no secret resolver configured", field, ref)
	}
	v, err := r.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve profile store field %q from %q: %w", field, ref, err)
	}
	return v, nil
}

// FromProfileStore resolves a profile's store definition into a complete
// client configuration, reading every secret reference it names through r.
//
// This is the whole of what a profiles file says about reaching a repository:
// where the store is, and which credentials open it. Fields the profile does
// not mention are left at their zero value, which is the correct default (see
// the package comment), so the result is directly usable — pass it to
// pkg/open, or inspect it, without further filling in.
//
// Secret references are resolved here rather than at connect time, so a
// profile naming a secret that cannot be read fails while it is still obvious
// which profile and which field are at fault, instead of surfacing later as an
// authentication failure from a cloud provider.
//
// Callers layering their own precedence on top — the cloudstic CLI folds
// command-line flags over this — should resolve the profile first and then
// override, rather than pre-filling and expecting this to defer.
// It is ApplyProfileStore against an empty configuration with nothing
// overridden, and is defined that way rather than reimplemented: on a zero
// Client the two groups ApplyProfileStore distinguishes collapse into the same
// behaviour, so a separate implementation would be a second copy of the field
// list waiting to disagree with the first.
func FromProfileStore(ctx context.Context, s profile.Store, r *secretref.Resolver) (Client, error) {
	var cfg Client
	if err := ApplyProfileStore(ctx, &cfg, s, r, nil); err != nil {
		return Client{}, err
	}
	return cfg, nil
}

// secretField binds one profile credential field to where its resolved value
// belongs in a Client.
type secretField struct {
	// flag is the cloudstic flag that overrides this field. It is meaningless
	// to this package and is carried only so the CLI can drive the same table
	// rather than maintaining a second copy that could drift from this one.
	flag   string
	field  string
	inline string
	ref    string
	dest   *string
}

// secretFields is the single table of which profile fields carry credentials,
// where each one lands, and which flag supersedes it. Both this package's
// FromProfileStore and the CLI's flag-precedence fold read it, so the two
// cannot disagree about the set of fields or where they go.
func secretFields(s profile.Store, cfg *Client) []secretField {
	return []secretField{
		{"s3-profile", "s3_profile", s.S3Profile, "", &cfg.Store.S3.Profile},
		{"s3-access-key", "s3_access_key", s.S3AccessKey, s.S3AccessKeySecret, &cfg.Store.S3.AccessKey},
		{"s3-secret-key", "s3_secret_key", s.S3SecretKey, s.S3SecretKeySecret, &cfg.Store.S3.SecretKey},
		{"b2-key-id", "b2_key_id", s.B2KeyID, s.B2KeyIDSecret, &cfg.Store.B2.KeyID},
		{"b2-app-key", "b2_app_key", s.B2AppKey, s.B2AppKeySecret, &cfg.Store.B2.AppKey},
		{"store-sftp-password", "store_sftp_password", s.StoreSFTPPassword, s.StoreSFTPPasswordSecret, &cfg.Store.SFTP.Password},
		{"store-sftp-key", "store_sftp_key", s.StoreSFTPKey, s.StoreSFTPKeySecret, &cfg.Store.SFTP.Key},
		{"password", "password", "", s.PasswordSecret, &cfg.Unlock.Password},
		{"encryption-key", "encryption_key", "", s.EncryptionKeySecret, &cfg.Unlock.EncryptionKey},
		{"recovery-key", "recovery_key", "", s.RecoveryKeySecret, &cfg.Unlock.RecoveryKey},
	}
}

// ApplyProfileStore folds a profile's store definition into an existing
// configuration, leaving alone any field the caller has already decided.
//
// overridden reports whether the caller has its own value for a field, named
// by the cloudstic flag that carries it ("store", "s3-access-key", …). A nil
// overridden means nothing is overridden, making this equivalent to
// FromProfileStore.
//
// The two groups of fields deliberately behave differently, and the difference
// is load-bearing:
//
//   - Location and KMS settings are taken only when the profile actually names
//     one, so a profile that is silent about them leaves whatever the caller
//     had.
//   - Credentials are taken even when the profile is silent, which clears a
//     value the caller had. That is what makes selecting a profile override an
//     ambient credential from the environment: a profile is an explicit choice
//     of *which* store to talk to, so inheriting half a credential set from
//     the environment would be a way to reach it with the wrong identity.
//
// A field the caller has overridden is never resolved, so a broken secret
// reference on a field that is about to be replaced is not an error.
func ApplyProfileStore(ctx context.Context, cfg *Client, s profile.Store, r *secretref.Resolver, overridden func(flag string) bool) error {
	if overridden == nil {
		overridden = func(string) bool { return false }
	}

	// Taken only when the profile names a value.
	for _, f := range []struct {
		flag string
		val  string
		dest *string
	}{
		{"store", s.URI, &cfg.Store.URI},
		{"s3-endpoint", s.S3Endpoint, &cfg.Store.S3.Endpoint},
		{"s3-region", s.S3Region, &cfg.Store.S3.Region},
		{"kms-key-arn", s.KMSKeyARN, &cfg.Unlock.KMS.KeyARN},
		{"kms-region", s.KMSRegion, &cfg.Unlock.KMS.Region},
		{"kms-endpoint", s.KMSEndpoint, &cfg.Unlock.KMS.Endpoint},
	} {
		if !overridden(f.flag) && f.val != "" {
			*f.dest = f.val
		}
	}

	// Taken whenever the caller has not overridden the field, empty included.
	for _, f := range secretFields(s, cfg) {
		if overridden(f.flag) {
			continue
		}
		v, err := ResolveValue(ctx, r, f.field, f.inline, f.ref)
		if err != nil {
			return err
		}
		*f.dest = v
	}
	return nil
}
