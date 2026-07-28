package main

import (
	"github.com/cloudstic/cli/pkg/secretref"
	secretrefbackends "github.com/cloudstic/cli/pkg/secretref/backends"
)

// newSecretResolver builds the set of secret-reference schemes this CLI
// understands, with the config-token backend pointed at configDir.
//
// pkg/config takes a resolver as a parameter rather than defaulting to one, so
// choosing it is the composition root's job — which for this binary means here.
// Default() returns a fresh map, so a build that wanted an extra scheme would
// add to it here rather than this being the only set anyone can have.
//
// It is a function rather than a package-level value because the answer
// depends on -config-dir: config-token:// references name a token in the
// config directory's managed store, so pointing the CLI at a different
// directory has to point those references there too. A process-wide value
// would have to be reassigned after flag parsing, which is the kind of
// write-once-and-hope global RFC 0022 §8 argues against creating.
//
// configDir carries paths.ConfigDir's meaning: empty means "use
// CLOUDSTIC_CONFIG_DIR or the platform default".
func newSecretResolver(configDir string) *secretref.Resolver {
	backends := secretrefbackends.Default()
	backends["config-token"] = secretrefbackends.NewConfigTokenBackend(
		secretrefbackends.WithConfigDir(configDir))
	return secretref.NewResolver(backends)
}
