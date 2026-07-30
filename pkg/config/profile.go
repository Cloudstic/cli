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
// key names the profiles-file entry and appears in the error, so a failure
// says which entry to go fix rather than only which backend refused.
func ResolveValue(ctx context.Context, r *secretref.Resolver, key, inline, ref string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if ref == "" {
		return "", nil
	}
	if r == nil {
		return "", fmt.Errorf("resolve profile store field %q from %q: no secret resolver configured", key, ref)
	}
	v, err := r.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve profile store field %q from %q: %w", key, ref, err)
	}
	return v, nil
}

// FromProfileStore resolves a profile's store definition into a complete client
// configuration, reading every secret reference it names through r.
//
// This is the whole of what a profiles file says about reaching a repository:
// where the store is, and which credentials open it. Fields the profile does
// not mention are left at their zero value, which is the correct default (see
// the package comment), so the result is directly usable — pass it to pkg/open,
// or inspect it, without further filling in.
//
// Secret references are resolved here rather than at connect time, so a profile
// naming a secret that cannot be read fails while it is still obvious which
// profile and which field are at fault, instead of surfacing later as an
// authentication failure from a cloud provider.
//
// Use MergeProfileStore when you have configuration of your own to layer over
// the profile. This is that function with nothing decided, and is defined that
// way rather than reimplemented: against a zero Client the two field groups it
// distinguishes collapse into the same behaviour, so a separate implementation
// would be a second copy of the field list waiting to disagree with the first.
func FromProfileStore(ctx context.Context, s profile.Store, r *secretref.Resolver) (Client, error) {
	return MergeProfileStore(ctx, Client{}, nil, s, r)
}

// MergeProfileStore returns base with the profile's store definition folded in
// underneath it: every field the caller has already decided is left alone, and
// the rest come from the profile.
//
// decided names the fields the caller owns. Pass FieldsSetIn(base) when a
// non-empty value is what "I decided this" means for your mechanism, or an
// explicit NewFieldSet when empty is a choice you need to keep. A nil set
// decides nothing, making this equivalent to FromProfileStore.
//
// The two groups of fields deliberately behave differently, and the difference
// is load-bearing:
//
//   - Location and KMS settings are taken only when the profile actually names
//     one, so a profile that is silent about them leaves whatever base had.
//   - Credentials are taken whenever the caller has not decided them, which
//     clears a value base carried. That is what makes selecting a profile
//     override an ambient credential from the environment: a profile is an
//     explicit choice of *which* store to talk to, so reaching it with half a
//     credential set inherited from the environment would be worse than failing
//     to reach it at all.
//
// A decided field is never resolved, so a broken secret reference on a field
// that is about to be replaced is not an error.
func MergeProfileStore(ctx context.Context, base Client, decided FieldSet, s profile.Store, r *secretref.Resolver) (Client, error) {
	cfg := base
	for _, spec := range fieldSpecs() {
		if decided.Has(spec.field) {
			continue
		}
		inline, ref := spec.read(s)

		if spec.kind == kindLocation {
			// No reference to read, and silence means "leave base alone".
			if inline != "" {
				*spec.dest(&cfg) = inline
			}
			continue
		}

		v, err := ResolveValue(ctx, r, spec.key, inline, ref)
		if err != nil {
			return Client{}, err
		}
		*spec.dest(&cfg) = v
	}
	return cfg, nil
}
