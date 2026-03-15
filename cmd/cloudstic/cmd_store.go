package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/secretref"
)

var validRefName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func (r *runner) runStore() int {
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic store <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: list, show, new")
		return 1
	}

	switch os.Args[2] {
	case "list":
		return r.runStoreList()
	case "show":
		return r.runStoreShow()
	case "new":
		return r.runStoreNew()
	default:
		return r.fail("Unknown store subcommand: %s", os.Args[2])
	}
}

func (r *runner) runStoreList() int {
	fs := flag.NewFlagSet("store list", flag.ExitOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	names := make([]string, 0, len(cfg.Stores))
	for name := range cfg.Stores {
		names = append(names, name)
	}
	sort.Strings(names)

	_, _ = fmt.Fprintf(r.out, "%d stores\n", len(names))
	for _, name := range names {
		s := cfg.Stores[name]
		_, _ = fmt.Fprintf(r.out, "- %s", name)
		if s.URI != "" {
			_, _ = fmt.Fprintf(r.out, "  uri=%s", s.URI)
		}
		_, _ = fmt.Fprintln(r.out)
	}
	return 0
}

func (r *runner) runStoreShow() int {
	fs := flag.NewFlagSet("store show", flag.ExitOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))
	if fs.NArg() > 1 {
		return r.fail("usage: cloudstic store show [-profiles-file <path>] <name>")
	}

	name := ""
	if fs.NArg() == 1 {
		name = fs.Arg(0)
	}

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic store show [-profiles-file <path>] <name>")
		}
		names := make([]string, 0, len(cfg.Stores))
		for n := range cfg.Stores {
			names = append(names, n)
		}
		sort.Strings(names)
		picked, pickErr := r.promptSelect("Select store", names)
		if pickErr != nil {
			return r.fail("Failed to select store: %v", pickErr)
		}
		name = picked
	}

	s, ok := cfg.Stores[name]
	if !ok {
		return r.fail("Unknown store %q", name)
	}

	_, _ = fmt.Fprintf(r.out, "store: %s\n", name)
	_, _ = fmt.Fprintf(r.out, "  uri: %s\n", s.URI)
	_, _ = fmt.Fprintf(r.out, "  auth_mode: %s\n", profileStoreAuthMode(s))
	if s.S3Region != "" {
		_, _ = fmt.Fprintf(r.out, "  s3_region: %s\n", s.S3Region)
	}
	if s.S3Profile != "" {
		_, _ = fmt.Fprintf(r.out, "  s3_profile: %s\n", s.S3Profile)
	}
	if s.S3Endpoint != "" {
		_, _ = fmt.Fprintf(r.out, "  s3_endpoint: %s\n", s.S3Endpoint)
	}
	if s.S3AccessKeyEnv != "" {
		_, _ = fmt.Fprintf(r.out, "  s3_access_key_env (deprecated): %s\n", s.S3AccessKeyEnv)
	}
	if s.S3AccessKeySecret != "" {
		_, _ = fmt.Fprintf(r.out, "  s3_access_key_secret: %s\n", s.S3AccessKeySecret)
	}
	if s.S3SecretKeyEnv != "" {
		_, _ = fmt.Fprintf(r.out, "  s3_secret_key_env (deprecated): %s\n", s.S3SecretKeyEnv)
	}
	if s.S3SecretKeySecret != "" {
		_, _ = fmt.Fprintf(r.out, "  s3_secret_key_secret: %s\n", s.S3SecretKeySecret)
	}
	if s.S3ProfileEnv != "" {
		_, _ = fmt.Fprintf(r.out, "  s3_profile_env: %s\n", s.S3ProfileEnv)
	}
	if s.StoreSFTPPasswordEnv != "" {
		_, _ = fmt.Fprintf(r.out, "  store_sftp_password_env (deprecated): %s\n", s.StoreSFTPPasswordEnv)
	}
	if s.StoreSFTPPasswordSecret != "" {
		_, _ = fmt.Fprintf(r.out, "  store_sftp_password_secret: %s\n", s.StoreSFTPPasswordSecret)
	}
	if s.StoreSFTPKeyEnv != "" {
		_, _ = fmt.Fprintf(r.out, "  store_sftp_key_env (deprecated): %s\n", s.StoreSFTPKeyEnv)
	}
	if s.StoreSFTPKeySecret != "" {
		_, _ = fmt.Fprintf(r.out, "  store_sftp_key_secret: %s\n", s.StoreSFTPKeySecret)
	}
	if s.PasswordEnv != "" {
		_, _ = fmt.Fprintf(r.out, "  password_env (deprecated): %s\n", s.PasswordEnv)
	}
	if s.PasswordSecret != "" {
		_, _ = fmt.Fprintf(r.out, "  password_secret: %s\n", s.PasswordSecret)
	}
	if s.EncryptionKeyEnv != "" {
		_, _ = fmt.Fprintf(r.out, "  encryption_key_env (deprecated): %s\n", s.EncryptionKeyEnv)
	}
	if s.EncryptionKeySecret != "" {
		_, _ = fmt.Fprintf(r.out, "  encryption_key_secret: %s\n", s.EncryptionKeySecret)
	}
	if s.RecoveryKeyEnv != "" {
		_, _ = fmt.Fprintf(r.out, "  recovery_key_env (deprecated): %s\n", s.RecoveryKeyEnv)
	}
	if s.RecoveryKeySecret != "" {
		_, _ = fmt.Fprintf(r.out, "  recovery_key_secret: %s\n", s.RecoveryKeySecret)
	}
	if s.KMSKeyARN != "" {
		_, _ = fmt.Fprintf(r.out, "  kms_key_arn: %s\n", s.KMSKeyARN)
	}
	if s.KMSRegion != "" {
		_, _ = fmt.Fprintf(r.out, "  kms_region: %s\n", s.KMSRegion)
	}
	if s.KMSEndpoint != "" {
		_, _ = fmt.Fprintf(r.out, "  kms_endpoint: %s\n", s.KMSEndpoint)
	}

	// Show which profiles reference this store.
	var refs []string
	for pName, p := range cfg.Profiles {
		if p.Store == name {
			refs = append(refs, pName)
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		_, _ = fmt.Fprintf(r.out, "  used_by: %v\n", refs)
	}
	return 0
}

func (r *runner) runStoreNew() int {
	fs := flag.NewFlagSet("store new", flag.ExitOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	name := fs.String("name", "", "Store reference name")
	uri := fs.String("uri", "", "Store URI (e.g. s3:bucket/path, local:/path, sftp://host/path)")
	s3Region := fs.String("s3-region", "", "S3 region")
	s3Profile := fs.String("s3-profile", "", "AWS shared config profile")
	s3Endpoint := fs.String("s3-endpoint", "", "S3-compatible endpoint URL")
	s3AccessKey := fs.String("s3-access-key", "", "S3 static access key")
	s3SecretKey := fs.String("s3-secret-key", "", "S3 static secret key")
	s3AccessKeyEnv := fs.String("s3-access-key-env", "", "Env var name for S3 access key")
	s3SecretKeyEnv := fs.String("s3-secret-key-env", "", "Env var name for S3 secret key")
	s3ProfileEnv := fs.String("s3-profile-env", "", "Env var name for AWS profile")
	sftpPassword := fs.String("store-sftp-password", "", "SFTP password")
	sftpKey := fs.String("store-sftp-key", "", "Path to SFTP private key")
	sftpPasswordEnv := fs.String("store-sftp-password-env", "", "Env var name for SFTP password")
	sftpKeyEnv := fs.String("store-sftp-key-env", "", "Env var name for SFTP key path")
	passwordEnv := fs.String("password-env", "", "Env var name for repository password")
	encryptionKeyEnv := fs.String("encryption-key-env", "", "Env var name for platform key (hex)")
	recoveryKeyEnv := fs.String("recovery-key-env", "", "Env var name for recovery key mnemonic")
	kmsKeyARN := fs.String("kms-key-arn", "", "AWS KMS key ARN")
	kmsRegion := fs.String("kms-region", "", "AWS KMS region")
	kmsEndpoint := fs.String("kms-endpoint", "", "Custom AWS KMS endpoint URL")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))

	if *name == "" {
		if r.canPrompt() {
			v, err := r.promptLine("Store reference name", "")
			if err != nil {
				return r.fail("Failed to read store name: %v", err)
			}
			*name = v
		}
		if *name == "" {
			return r.fail("-name is required")
		}
	}
	if !validRefName.MatchString(*name) {
		return r.fail("invalid store name %q: must start with a letter or digit and contain only letters, digits, dots, hyphens, or underscores", *name)
	}
	if *uri == "" {
		if r.canPrompt() {
			v, err := r.promptLine("Store URI", "")
			if err != nil {
				return r.fail("Failed to read store URI: %v", err)
			}
			*uri = v
		}
		if *uri == "" {
			return r.fail("-uri is required")
		}
	}

	// Validate the URI before saving.
	if _, err := parseStoreURI(*uri); err != nil {
		return r.fail("%v", err)
	}

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg = &cloudstic.ProfilesConfig{Version: 1}
		} else {
			return r.fail("Failed to load profiles: %v", err)
		}
	}
	if cfg.Stores == nil {
		cfg.Stores = map[string]cloudstic.ProfileStore{}
	}

	cfg.Stores[*name] = cloudstic.ProfileStore{
		URI:                     *uri,
		S3Region:                *s3Region,
		S3Profile:               *s3Profile,
		S3Endpoint:              *s3Endpoint,
		S3AccessKey:             *s3AccessKey,
		S3SecretKey:             *s3SecretKey,
		S3AccessKeyEnv:          "",
		S3SecretKeyEnv:          "",
		S3AccessKeySecret:       envRef(*s3AccessKeyEnv),
		S3SecretKeySecret:       envRef(*s3SecretKeyEnv),
		S3ProfileEnv:            *s3ProfileEnv,
		StoreSFTPPassword:       *sftpPassword,
		StoreSFTPKey:            *sftpKey,
		StoreSFTPPasswordEnv:    "",
		StoreSFTPKeyEnv:         "",
		StoreSFTPPasswordSecret: envRef(*sftpPasswordEnv),
		StoreSFTPKeySecret:      envRef(*sftpKeyEnv),
		PasswordEnv:             "",
		EncryptionKeyEnv:        "",
		RecoveryKeyEnv:          "",
		PasswordSecret:          envRef(*passwordEnv),
		EncryptionKeySecret:     envRef(*encryptionKeyEnv),
		RecoveryKeySecret:       envRef(*recoveryKeyEnv),
		KMSKeyARN:               *kmsKeyARN,
		KMSRegion:               *kmsRegion,
		KMSEndpoint:             *kmsEndpoint,
	}

	if err := cloudstic.SaveProfilesFile(*profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}
	_, _ = fmt.Fprintf(r.out, "Store %q saved in %s\n", *name, *profilesFile)

	if r.canPrompt() {
		// If no encryption flags were provided, prompt for encryption config.
		s := cfg.Stores[*name]
		hasExplicitEncryption := s.PasswordEnv != "" || s.EncryptionKeyEnv != "" ||
			s.RecoveryKeyEnv != "" || s.PasswordSecret != "" ||
			s.EncryptionKeySecret != "" || s.RecoveryKeySecret != "" ||
			s.KMSKeyARN != ""
		if !hasExplicitEncryption {
			r.promptEncryptionConfig(cfg, *name, *profilesFile)
		}
		r.checkOrInitStore(cfg, *name, *profilesFile)
	}

	return 0
}

// checkOrInitStore connects to a store and checks if it is initialized.
// If already initialized, it confirms connectivity. If not, it offers to
// initialize it. Encryption config should already be saved in profiles.yaml
// before calling this. Errors are printed but never cause a non-zero exit—
// the store config has already been saved.
func (r *runner) checkOrInitStore(cfg *cloudstic.ProfilesConfig, storeName, profilesFile string) {
	s := cfg.Stores[storeName]
	g := globalFlagsFromProfileStore(s)
	raw, err := g.initObjectStore()
	if err != nil {
		_, _ = fmt.Fprintf(r.errOut, "Could not connect to store: %v\n", err)
		return
	}

	ctx := context.Background()

	// Check if already initialized by looking for the config marker.
	cfgData, err := raw.Get(ctx, "config")
	if err == nil && cfgData != nil {
		_, _ = fmt.Fprintln(r.out, "Store is already initialized and accessible.")
		return
	}

	_, _ = fmt.Fprintln(r.out, "Store is accessible but not yet initialized.")
	yes, promptErr := r.promptConfirm("Initialize it now?", true)
	if promptErr != nil || !yes {
		return
	}

	// Check if the store has encryption config.
	hasEncryption := s.PasswordEnv != "" || s.EncryptionKeyEnv != "" ||
		s.RecoveryKeyEnv != "" || s.PasswordSecret != "" ||
		s.EncryptionKeySecret != "" || s.RecoveryKeySecret != "" ||
		s.KMSKeyARN != ""

	if !hasEncryption {
		// No encryption configured — init without encryption.
		result, initErr := cloudstic.InitRepo(ctx, raw, cloudstic.WithInitNoEncryption())
		if initErr != nil {
			_, _ = fmt.Fprintf(r.errOut, "Init failed: %v\n", initErr)
			return
		}
		r.printInitResult(result)
		return
	}

	// Build keychain from the store's encryption settings.
	// For password-based encryption, the env var must be set for init.
	// If not set, prompt for the password interactively.
	kc, err := g.buildKeychain(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(r.errOut, "Failed to build keychain: %v\n", err)
		return
	}

	var initOpts []cloudstic.InitOption
	initOpts = append(initOpts, cloudstic.WithInitCredentials(kc))
	result, err := cloudstic.InitRepo(ctx, raw, initOpts...)
	if err != nil {
		_, _ = fmt.Fprintf(r.errOut, "Init failed: %v\n", err)
		return
	}
	r.printInitResult(result)
}

// promptEncryptionConfig guides the user through encryption configuration
// and saves the chosen settings to profiles.yaml. It does not build a keychain
// or prompt for the actual password — that happens later during init.
func (r *runner) promptEncryptionConfig(cfg *cloudstic.ProfilesConfig, storeName, profilesFile string) {
	_, _ = fmt.Fprintln(r.out)
	_, _ = fmt.Fprintln(r.out, "No encryption is configured for this store.")

	options := []string{
		"Password (recommended for interactive use)",
		"Platform key (recommended for automation/CI)",
		"AWS KMS key (enterprise)",
		"No encryption (not recommended)",
	}
	picked, err := r.promptSelect("Select encryption method", options)
	if err != nil {
		_, _ = fmt.Fprintf(r.errOut, "Failed to select encryption method: %v\n", err)
		return
	}

	s := cfg.Stores[storeName]

	switch picked {
	case options[0]: // Password
		envName, envErr := r.promptLine("Env var name for the repository password", "CLOUDSTIC_PASSWORD")
		if envErr != nil {
			_, _ = fmt.Fprintf(r.errOut, "Failed to read env var name: %v\n", envErr)
			return
		}
		if envName == "" {
			envName = "CLOUDSTIC_PASSWORD"
		}
		s.PasswordEnv = ""
		s.PasswordSecret = envRef(envName)
		_, _ = fmt.Fprintf(r.out, "Encryption: password via $%s\n", envName)

	case options[1]: // Platform key
		envName, envErr := r.promptLine("Env var name for the platform key (64-char hex)", "CLOUDSTIC_ENCRYPTION_KEY")
		if envErr != nil {
			_, _ = fmt.Fprintf(r.errOut, "Failed to read env var name: %v\n", envErr)
			return
		}
		if envName == "" {
			envName = "CLOUDSTIC_ENCRYPTION_KEY"
		}
		s.EncryptionKeyEnv = ""
		s.EncryptionKeySecret = envRef(envName)
		_, _ = fmt.Fprintf(r.out, "Encryption: platform key via $%s\n", envName)

	case options[2]: // KMS
		arn, arnErr := r.promptLine("KMS key ARN", "")
		if arnErr != nil || arn == "" {
			_, _ = fmt.Fprintln(r.errOut, "KMS key ARN is required.")
			return
		}
		s.KMSKeyARN = arn

		region, _ := r.promptLine("KMS region", "us-east-1")
		if region != "" {
			s.KMSRegion = region
		}
		_, _ = fmt.Fprintf(r.out, "Encryption: AWS KMS (%s)\n", arn)

	case options[3]: // No encryption
		_, _ = fmt.Fprintln(r.out, "Encryption: none (not recommended)")
		return
	}

	// Save updated store config.
	cfg.Stores[storeName] = s
	if saveErr := cloudstic.SaveProfilesFile(profilesFile, cfg); saveErr != nil {
		_, _ = fmt.Fprintf(r.errOut, "Warning: could not save encryption settings: %v\n", saveErr)
	}
}

// globalFlagsFromProfileStore builds a globalFlags populated from a ProfileStore,
// resolving env var indirections for secrets.
func globalFlagsFromProfileStore(s cloudstic.ProfileStore) *globalFlags {
	resolver := secretref.NewDefaultResolver()
	resolve := func(direct, secretRef, envName string) string {
		if direct != "" {
			return direct
		}
		if secretRef != "" {
			v, err := resolver.Resolve(context.Background(), secretRef)
			if err == nil {
				return v
			}
		}
		if envName != "" {
			return os.Getenv(envName)
		}
		return ""
	}

	g := &globalFlags{}
	store := s.URI
	g.store = &store
	s3Endpoint := s.S3Endpoint
	g.s3Endpoint = &s3Endpoint
	s3Region := s.S3Region
	if s3Region == "" {
		s3Region = "us-east-1"
	}
	g.s3Region = &s3Region
	s3Profile := resolve(s.S3Profile, "", s.S3ProfileEnv)
	g.s3Profile = &s3Profile
	s3AccessKey := resolve(s.S3AccessKey, s.S3AccessKeySecret, s.S3AccessKeyEnv)
	g.s3AccessKey = &s3AccessKey
	s3SecretKey := resolve(s.S3SecretKey, s.S3SecretKeySecret, s.S3SecretKeyEnv)
	g.s3SecretKey = &s3SecretKey
	storeSFTPPassword := resolve(s.StoreSFTPPassword, s.StoreSFTPPasswordSecret, s.StoreSFTPPasswordEnv)
	g.storeSFTPPassword = &storeSFTPPassword
	storeSFTPKey := resolve(s.StoreSFTPKey, s.StoreSFTPKeySecret, s.StoreSFTPKeyEnv)
	g.storeSFTPKey = &storeSFTPKey
	password := resolve("", s.PasswordSecret, s.PasswordEnv)
	g.password = &password
	encryptionKey := resolve("", s.EncryptionKeySecret, s.EncryptionKeyEnv)
	g.encryptionKey = &encryptionKey
	recoveryKey := resolve("", s.RecoveryKeySecret, s.RecoveryKeyEnv)
	g.recoveryKey = &recoveryKey
	kmsKeyARN := s.KMSKeyARN
	g.kmsKeyARN = &kmsKeyARN
	kmsRegion := s.KMSRegion
	g.kmsRegion = &kmsRegion
	kmsEndpoint := s.KMSEndpoint
	g.kmsEndpoint = &kmsEndpoint

	// Non-store fields with safe defaults.
	empty := ""
	g.sourceSFTPPassword = &empty
	g.sourceSFTPKey = &empty
	falseVal := false
	g.disablePackfile = &falseVal
	g.prompt = &falseVal
	g.verbose = &falseVal
	g.quiet = &falseVal
	g.debug = &falseVal
	g.profile = &empty
	g.profilesFile = &empty

	return g
}

func envRef(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "env://" + name
}
