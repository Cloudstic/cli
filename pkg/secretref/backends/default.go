// Package backends holds the secret backends Cloudstic ships with, split out
// from the pkg/secretref contract so that implementing a custom backend does
// not drag in the platform-native ones (macOS Keychain, libsecret, Windows
// Credential Manager) and their build constraints.
//
// Callers who want to *add* a scheme rather than replace the built-in set
// start from Default:
//
//	b := backends.Default()
//	b["vault"] = myVaultBackend{}
//	resolver := secretref.NewResolver(b)
package backends

import "github.com/cloudstic/cli/pkg/secretref"

// Default returns a fresh map of the built-in backends, keyed by scheme. The
// map is newly allocated on each call, so callers may add to or remove from it
// without affecting anyone else.
//
// Replacing an entry is also how you reconfigure a built-in. The config-token
// backend writes into Cloudstic's own config directory by default, which a
// program embedding this package usually does not want:
//
//	b := backends.Default()
//	b["config-token"] = backends.NewConfigTokenBackend(backends.WithConfigDir(myDir))
//	resolver := secretref.NewResolver(b)
func Default() map[string]secretref.Backend {
	return map[string]secretref.Backend{
		"env":            NewEnvBackend(nil),
		"file":           NewFileBackend(),
		"config-token":   NewConfigTokenBackend(),
		"keychain":       NewKeychainBackend(),
		"wincred":        NewWincredBackend(),
		"secret-service": NewSecretServiceBackend(),
	}
}

// NewDefaultResolver builds the standard resolver over Default.
func NewDefaultResolver() *secretref.Resolver {
	return secretref.NewResolver(Default())
}
