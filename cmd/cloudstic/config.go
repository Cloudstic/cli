package main

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
)

// The types in this file are the resolved configuration a command runs with,
// as distinct from globalFlags, which holds only what was parsed from the
// command line and the environment.
//
// Resolution is an explicit, non-mutating step: resolveClientConfig reads a
// globalFlags value and returns a clientConfig with any profile values folded
// in. Parsed flags are never rewritten, so a caller can always tell what the
// user actually typed, and opening a store or client has no side effects on
// command input.
//
// Construction (storeuri.go, storebuild.go, keychain.go) consumes these values
// and never sees globalFlags, which is what makes it unit-testable without
// going through flag parsing.

// The resolved configuration types now live in pkg/config, so that a caller
// outside this module can build the same values the CLI does. They are aliased
// rather than renamed at every use site: an alias is the same type, so these
// spellings and pkg/config's stay interchangeable while the remaining
// resolution logic in this file moves across (RFC 0022 §7).
type (
	s3Config     = config.S3
	b2Config     = config.B2
	sftpConfig   = config.SFTP
	kmsConfig    = config.KMS
	storeConfig  = config.Store
	unlockConfig = config.Unlock
	clientConfig = config.Client
)

// clientConfigFromFlags projects parsed flags into a resolved configuration,
// without consulting any profile. It is a pure translation: no I/O, no
// mutation of g.
func clientConfigFromFlags(g *globalFlags) clientConfig {
	return clientConfig{
		Store: storeConfig{
			URI: g.store,
			S3: s3Config{
				Endpoint:  g.s3Endpoint,
				Region:    g.s3Region,
				Profile:   g.s3Profile,
				AccessKey: g.s3AccessKey,
				SecretKey: g.s3SecretKey,
			},
			B2: b2Config{
				KeyID:  g.b2KeyID,
				AppKey: g.b2AppKey,
			},
			SFTP: sftpConfig{
				Password:   g.storeSFTPPassword,
				Key:        g.storeSFTPKey,
				KnownHosts: g.storeSFTPKnownHosts,
				Insecure:   g.storeSFTPInsecure,
			},
			Debug: g.debug,
		},
		Unlock: unlockConfig{
			Password:      g.password,
			EncryptionKey: g.encryptionKey,
			RecoveryKey:   g.recoveryKey,
			KMS: kmsConfig{
				KeyARN:   g.kmsKeyARN,
				Region:   g.kmsRegion,
				Endpoint: g.kmsEndpoint,
			},
			Prompt:   g.prompt,
			NoPrompt: g.noPrompt,
		},
		DisablePackfile: g.disablePackfile,
		Quiet:           g.quiet,
		JSON:            g.json,
	}
}

// sourceSFTPConfigFromFlags projects the source-side SFTP flags. The source is
// configured separately from the store and is not part of clientConfig, since
// only backup reads one.
func sourceSFTPConfigFromFlags(g *globalFlags) sftpConfig {
	return sftpConfig{
		Password:   g.sourceSFTPPassword,
		Key:        g.sourceSFTPKey,
		KnownHosts: g.sourceSFTPKnownHosts,
		Insecure:   g.sourceSFTPInsecure,
	}
}

// resolveClientConfig produces the configuration a command should run with:
// parsed flags, with the selected profile's store folded in where the user did
// not pass an explicit flag. It reads profiles.yaml when -profile is set, and
// leaves g untouched.
func resolveClientConfig(g *globalFlags) (clientConfig, error) {
	cfg := clientConfigFromFlags(g)
	s, err := loadProfileStore(g)
	if err != nil {
		return clientConfig{}, err
	}
	if s == nil {
		return cfg, nil
	}
	if err := applyProfileStore(&cfg, *s, g.configDir, g.flagProvided); err != nil {
		return clientConfig{}, err
	}
	return cfg, nil
}

// loadProfileStore returns the store configuration for g's selected profile, or
// nil when no profile is selected or the profile names no store.
func loadProfileStore(g *globalFlags) (*profile.Store, error) {
	if g.profile == "" {
		return nil, nil
	}
	profilesFile := defaultProfilesFilename
	if g.profilesFile != "" {
		profilesFile = g.profilesFile
	}
	cfg, err := profile.Load(profilesFile)
	if err != nil {
		return nil, fmt.Errorf("load profiles file %q: %w", profilesFile, err)
	}
	p, ok := cfg.Profiles[g.profile]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", g.profile)
	}
	return lookupProfileStore(cfg, g.profile, p)
}

// lookupProfileStore returns profileName's referenced store, or nil if the
// profile names no store. Shared by loadProfileStore above, which resolves it
// fully to fold into a running command's config, and cmd_backup.go's
// mergeProfileBackupArgs, which only needs the existence check: the store
// itself is deliberately applied later, when resolveClientConfig runs for the
// backup that's actually about to happen (see mergeProfileBackupArgs).
func lookupProfileStore(cfg *profile.Config, profileName string, p profile.Profile) (*profile.Store, error) {
	if p.Store == "" {
		return nil, nil
	}
	s, ok := cfg.Stores[p.Store]
	if !ok {
		return nil, fmt.Errorf("profile %q references unknown store %q", profileName, p.Store)
	}
	return &s, nil
}

// clientConfigFromProfileStore builds a configuration from a store definition
// alone, for callers that address a store by name rather than through parsed
// flags (store verify, store init, the TUI). Nothing was passed on the command
// line, so every value comes from the profile.
//
// Not to be confused with tuiClientConfig (cmd_tui_activity.go), a thin
// quiet-mode wrapper around this same function, or with
// applyExistingStoreDefaults (store_builder_helpers.go), which prefills
// `store new`'s flags from an existing store entry for editing — an unrelated
// concept despite the similar name.
func clientConfigFromProfileStore(s profile.Store, configDir string) (clientConfig, error) {
	return config.FromProfileStore(context.Background(), s, newSecretResolver(configDir))
}

// applyProfileStore folds a profile's store definition into cfg, with any flag
// the user passed explicitly winning over the profile's value.
//
// The fold itself lives in pkg/config so that a caller outside this module
// applies the same rules; all this adds is what "explicitly passed" means
// here, which is a flag-parser concept and stays on this side of the boundary
// (RFC 0022 §7).
//
// This only covers store fields. Backup-specific profile fields that aren't
// part of a store — source, tags, excludes, native-source credentials, auth —
// go through cmd_backup.go's mergeProfileBackupArgs instead, applying the
// same "explicit flag beats profile value" precedence via its own
// flagProvided checks. The two aren't merged into one mechanism because their
// field types differ enough (bools, string slices, multi-field auth
// resolution vs. plain strings here) that forcing a shared table would
// obscure more than it clarifies.
func applyProfileStore(cfg *clientConfig, s profile.Store, configDir string, provided func(string) bool) error {
	return config.ApplyProfileStore(context.Background(), cfg, s, newSecretResolver(configDir), provided)
}
