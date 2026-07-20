package main

import (
	"fmt"

	cloudstic "github.com/cloudstic/cli"
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

// s3Config holds the credentials and endpoint settings for an S3 store.
type s3Config struct {
	endpoint  string
	region    string
	profile   string
	accessKey string
	secretKey string
}

// b2Config holds Backblaze B2 application key credentials.
type b2Config struct {
	keyID  string
	appKey string
}

// sftpConfig holds SFTP authentication and host-key settings. The same shape
// serves both a store and a backup source, which are configured independently.
type sftpConfig struct {
	password   string
	key        string
	knownHosts string
	insecure   bool
}

// kmsConfig holds AWS KMS settings for kms-platform key slots.
type kmsConfig struct {
	keyARN   string
	region   string
	endpoint string
}

// storeConfig is everything needed to construct an object store.
type storeConfig struct {
	uri   string
	s3    s3Config
	b2    b2Config
	sftp  sftpConfig
	debug bool
}

// unlockConfig is everything needed to build the keychain that unlocks a
// repository.
type unlockConfig struct {
	password      string
	encryptionKey string
	recoveryKey   string
	kms           kmsConfig
	prompt        bool
	noPrompt      bool
}

// clientConfig is the resolved configuration for opening a repository client.
type clientConfig struct {
	store    storeConfig
	unlock   unlockConfig
	packfile bool
	quiet    bool
	json     bool
}

// clientConfigFromFlags projects parsed flags into a resolved configuration,
// without consulting any profile. It is a pure translation: no I/O, no
// mutation of g.
func clientConfigFromFlags(g *globalFlags) clientConfig {
	return clientConfig{
		store: storeConfig{
			uri: g.store,
			s3: s3Config{
				endpoint:  g.s3Endpoint,
				region:    g.s3Region,
				profile:   g.s3Profile,
				accessKey: g.s3AccessKey,
				secretKey: g.s3SecretKey,
			},
			b2: b2Config{
				keyID:  g.b2KeyID,
				appKey: g.b2AppKey,
			},
			sftp: sftpConfig{
				password:   g.storeSFTPPassword,
				key:        g.storeSFTPKey,
				knownHosts: g.storeSFTPKnownHosts,
				insecure:   g.storeSFTPInsecure,
			},
			debug: g.debug,
		},
		unlock: unlockConfig{
			password:      g.password,
			encryptionKey: g.encryptionKey,
			recoveryKey:   g.recoveryKey,
			kms: kmsConfig{
				keyARN:   g.kmsKeyARN,
				region:   g.kmsRegion,
				endpoint: g.kmsEndpoint,
			},
			prompt:   g.prompt,
			noPrompt: g.noPrompt,
		},
		packfile: !g.disablePackfile,
		quiet:    g.quiet,
		json:     g.json,
	}
}

// sourceSFTPConfigFromFlags projects the source-side SFTP flags. The source is
// configured separately from the store and is not part of clientConfig, since
// only backup reads one.
func sourceSFTPConfigFromFlags(g *globalFlags) sftpConfig {
	return sftpConfig{
		password:   g.sourceSFTPPassword,
		key:        g.sourceSFTPKey,
		knownHosts: g.sourceSFTPKnownHosts,
		insecure:   g.sourceSFTPInsecure,
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
	if err := applyProfileStore(&cfg, *s, g.flagProvided); err != nil {
		return clientConfig{}, err
	}
	return cfg, nil
}

// loadProfileStore returns the store configuration for g's selected profile, or
// nil when no profile is selected or the profile names no store.
func loadProfileStore(g *globalFlags) (*cloudstic.ProfileStore, error) {
	if g.profile == "" {
		return nil, nil
	}
	profilesFile := defaultProfilesFilename
	if g.profilesFile != "" {
		profilesFile = g.profilesFile
	}
	cfg, err := cloudstic.LoadProfilesFile(profilesFile)
	if err != nil {
		return nil, fmt.Errorf("load profiles file %q: %w", profilesFile, err)
	}
	p, ok := cfg.Profiles[g.profile]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", g.profile)
	}
	if p.Store == "" {
		return nil, nil
	}
	s, ok := cfg.Stores[p.Store]
	if !ok {
		return nil, fmt.Errorf("profile %q references unknown store %q", g.profile, p.Store)
	}
	return &s, nil
}

// clientConfigFromProfileStore builds a configuration from a store definition
// alone, for callers that address a store by name rather than through parsed
// flags (store verify, store init, the TUI). Nothing was passed on the command
// line, so every value comes from the profile.
func clientConfigFromProfileStore(s cloudstic.ProfileStore) (clientConfig, error) {
	cfg := clientConfig{packfile: true}
	cfg.store.s3.region = defaultS3Region
	if err := applyProfileStore(&cfg, s, func(string) bool { return false }); err != nil {
		return clientConfig{}, err
	}
	return cfg, nil
}

// applyProfileStore folds a profile's store definition into cfg. provided
// reports whether a flag was passed explicitly on the command line; those
// values win over the profile. Secret references are resolved here, so a
// missing secret surfaces as an error at resolution time rather than as an
// empty credential at connect time.
func applyProfileStore(cfg *clientConfig, s cloudstic.ProfileStore, provided func(string) bool) error {
	if !provided("store") && s.URI != "" {
		cfg.store.uri = s.URI
	}
	if !provided("s3-endpoint") && s.S3Endpoint != "" {
		cfg.store.s3.endpoint = s.S3Endpoint
	}
	if !provided("s3-region") && s.S3Region != "" {
		cfg.store.s3.region = s.S3Region
	}
	if !provided("kms-key-arn") && s.KMSKeyARN != "" {
		cfg.unlock.kms.keyARN = s.KMSKeyARN
	}
	if !provided("kms-region") && s.KMSRegion != "" {
		cfg.unlock.kms.region = s.KMSRegion
	}
	if !provided("kms-endpoint") && s.KMSEndpoint != "" {
		cfg.unlock.kms.endpoint = s.KMSEndpoint
	}

	// Values that may be given inline or as a secret reference. Unlike the
	// fields above, these are assigned unconditionally once the flag was not
	// passed, which matches the behaviour these paths have always had.
	refs := []struct {
		flag   string
		field  string
		inline string
		secret string
		dest   *string
	}{
		{"s3-profile", "s3_profile", s.S3Profile, "", &cfg.store.s3.profile},
		{"s3-access-key", "s3_access_key", s.S3AccessKey, s.S3AccessKeySecret, &cfg.store.s3.accessKey},
		{"s3-secret-key", "s3_secret_key", s.S3SecretKey, s.S3SecretKeySecret, &cfg.store.s3.secretKey},
		{"b2-key-id", "b2_key_id", s.B2KeyID, s.B2KeyIDSecret, &cfg.store.b2.keyID},
		{"b2-app-key", "b2_app_key", s.B2AppKey, s.B2AppKeySecret, &cfg.store.b2.appKey},
		{"store-sftp-password", "store_sftp_password", s.StoreSFTPPassword, s.StoreSFTPPasswordSecret, &cfg.store.sftp.password},
		{"store-sftp-key", "store_sftp_key", s.StoreSFTPKey, s.StoreSFTPKeySecret, &cfg.store.sftp.key},
		{"password", "password", "", s.PasswordSecret, &cfg.unlock.password},
		{"encryption-key", "encryption_key", "", s.EncryptionKeySecret, &cfg.unlock.encryptionKey},
		{"recovery-key", "recovery_key", "", s.RecoveryKeySecret, &cfg.unlock.recoveryKey},
	}
	for _, r := range refs {
		if provided(r.flag) {
			continue
		}
		v, err := resolveProfileStoreValue(r.field, r.inline, r.secret)
		if err != nil {
			return err
		}
		*r.dest = v
	}
	return nil
}
