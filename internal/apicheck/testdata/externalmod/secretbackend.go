package externalmod

import (
	"context"

	"github.com/cloudstic/cli/pkg/secretref"
	"github.com/cloudstic/cli/pkg/secretref/backends"
)

// VaultBackend is the use case RFC 0022's follow-up targets: a third party
// adding a secret scheme without forking. Before pkg/secretref existed, neither
// secretref.Backend nor NewResolver was reachable from outside the module, so
// this could not be written at all.
type VaultBackend struct{}

var _ secretref.Backend = (*VaultBackend)(nil)

func (v *VaultBackend) Resolve(_ context.Context, ref secretref.Ref) (string, error) {
	if ref.Path == "" {
		return "", secretref.NewError(secretref.KindInvalidRef, ref.Raw, "empty vault path", nil)
	}
	return "secret-from-vault", nil
}

// NewResolverInOwnConfigDir is the other half of embedding: an external
// program needs its users' managed tokens under its own directory, not
// Cloudstic's. Without WithConfigDir the only lever is the CLOUDSTIC_CONFIG_DIR
// environment variable, which is process-wide and not something a library
// should be reaching for.
func NewResolverInOwnConfigDir(dir string) *secretref.Resolver {
	b := backends.Default()
	b["config-token"] = backends.NewConfigTokenBackend(backends.WithConfigDir(dir))
	return secretref.NewResolver(b)
}

// NewResolverWithVault shows the extending case — adding a scheme to the
// built-in set rather than replacing it, which is why backends.Default exists.
func NewResolverWithVault() *secretref.Resolver {
	b := backends.Default()
	b["vault"] = &VaultBackend{}
	return secretref.NewResolver(b)
}
