package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/secretref"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

var validRefName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func (r *runner) runStore() int {
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic store <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: list, show, new, verify")
		return 1
	}

	switch os.Args[2] {
	case "list":
		return r.runStoreList()
	case "show":
		return r.runStoreShow()
	case "new":
		return r.runStoreNew()
	case "verify":
		return r.runStoreVerify()
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
	s3AccessKeySecret := fs.String("s3-access-key-secret", "", "Secret reference for S3 access key (e.g. env://..., keychain://...)")
	s3SecretKeySecret := fs.String("s3-secret-key-secret", "", "Secret reference for S3 secret key (e.g. env://..., keychain://...)")
	s3AccessKeyEnv := fs.String("s3-access-key-env", "", "Env var name for S3 access key")
	s3SecretKeyEnv := fs.String("s3-secret-key-env", "", "Env var name for S3 secret key")
	s3ProfileEnv := fs.String("s3-profile-env", "", "Env var name for AWS profile")
	sftpPassword := fs.String("store-sftp-password", "", "SFTP password")
	sftpKey := fs.String("store-sftp-key", "", "Path to SFTP private key")
	sftpPasswordSecret := fs.String("store-sftp-password-secret", "", "Secret reference for SFTP password (e.g. env://..., keychain://...)")
	sftpKeySecret := fs.String("store-sftp-key-secret", "", "Secret reference for SFTP key path (e.g. env://..., keychain://...)")
	sftpPasswordEnv := fs.String("store-sftp-password-env", "", "Env var name for SFTP password")
	sftpKeyEnv := fs.String("store-sftp-key-env", "", "Env var name for SFTP key path")
	passwordSecret := fs.String("password-secret", "", "Secret reference for repository password (e.g. env://..., keychain://...)")
	encryptionKeySecret := fs.String("encryption-key-secret", "", "Secret reference for platform key (e.g. env://..., keychain://...)")
	recoveryKeySecret := fs.String("recovery-key-secret", "", "Secret reference for recovery key mnemonic (e.g. env://..., keychain://...)")
	passwordEnv := fs.String("password-env", "", "Env var name for repository password")
	encryptionKeyEnv := fs.String("encryption-key-env", "", "Env var name for platform key (hex)")
	recoveryKeyEnv := fs.String("recovery-key-env", "", "Env var name for recovery key mnemonic")
	kmsKeyARN := fs.String("kms-key-arn", "", "AWS KMS key ARN")
	kmsRegion := fs.String("kms-region", "", "AWS KMS region")
	kmsEndpoint := fs.String("kms-endpoint", "", "Custom AWS KMS endpoint URL")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))

	flagsSet := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { flagsSet[f.Name] = true })

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

	_, existedBefore := cfg.Stores[*name]
	forcePromptURI := false
	forcePromptEncryption := false
	askKeepEncryption := false
	if existing, ok := cfg.Stores[*name]; ok {
		if !flagsSet["uri"] && existing.URI != "" {
			*uri = existing.URI
		}
		if !flagsSet["s3-region"] && existing.S3Region != "" {
			*s3Region = existing.S3Region
		}
		if !flagsSet["s3-profile"] && existing.S3Profile != "" {
			*s3Profile = existing.S3Profile
		}
		if !flagsSet["s3-endpoint"] && existing.S3Endpoint != "" {
			*s3Endpoint = existing.S3Endpoint
		}
		if !flagsSet["s3-access-key"] && existing.S3AccessKey != "" {
			*s3AccessKey = existing.S3AccessKey
		}
		if !flagsSet["s3-secret-key"] && existing.S3SecretKey != "" {
			*s3SecretKey = existing.S3SecretKey
		}
		if !flagsSet["s3-access-key-secret"] && !flagsSet["s3-access-key-env"] {
			*s3AccessKeySecret = firstNonEmpty(existing.S3AccessKeySecret, envRef(existing.S3AccessKeyEnv))
		}
		if !flagsSet["s3-secret-key-secret"] && !flagsSet["s3-secret-key-env"] {
			*s3SecretKeySecret = firstNonEmpty(existing.S3SecretKeySecret, envRef(existing.S3SecretKeyEnv))
		}
		if !flagsSet["s3-profile-env"] && existing.S3ProfileEnv != "" {
			*s3ProfileEnv = existing.S3ProfileEnv
		}
		if !flagsSet["store-sftp-password"] && existing.StoreSFTPPassword != "" {
			*sftpPassword = existing.StoreSFTPPassword
		}
		if !flagsSet["store-sftp-key"] && existing.StoreSFTPKey != "" {
			*sftpKey = existing.StoreSFTPKey
		}
		if !flagsSet["store-sftp-password-secret"] && !flagsSet["store-sftp-password-env"] {
			*sftpPasswordSecret = firstNonEmpty(existing.StoreSFTPPasswordSecret, envRef(existing.StoreSFTPPasswordEnv))
		}
		if !flagsSet["store-sftp-key-secret"] && !flagsSet["store-sftp-key-env"] {
			*sftpKeySecret = firstNonEmpty(existing.StoreSFTPKeySecret, envRef(existing.StoreSFTPKeyEnv))
		}
		if !flagsSet["password-secret"] && !flagsSet["password-env"] {
			*passwordSecret = firstNonEmpty(existing.PasswordSecret, envRef(existing.PasswordEnv))
		}
		if !flagsSet["encryption-key-secret"] && !flagsSet["encryption-key-env"] {
			*encryptionKeySecret = firstNonEmpty(existing.EncryptionKeySecret, envRef(existing.EncryptionKeyEnv))
		}
		if !flagsSet["recovery-key-secret"] && !flagsSet["recovery-key-env"] {
			*recoveryKeySecret = firstNonEmpty(existing.RecoveryKeySecret, envRef(existing.RecoveryKeyEnv))
		}
		if !flagsSet["kms-key-arn"] && existing.KMSKeyARN != "" {
			*kmsKeyARN = existing.KMSKeyARN
		}
		if !flagsSet["kms-region"] && existing.KMSRegion != "" {
			*kmsRegion = existing.KMSRegion
		}
		if !flagsSet["kms-endpoint"] && existing.KMSEndpoint != "" {
			*kmsEndpoint = existing.KMSEndpoint
		}
		if promptURI, askKeep := existingStoreInteractivePlan(r.canPrompt(), hasStoreNewOverrideFlags(flagsSet), storeHasExplicitEncryption(existing)); promptURI {
			forcePromptURI = true
			askKeepEncryption = askKeep
		}
	}

	if *uri == "" || forcePromptURI {
		if r.canPrompt() {
			v, err := r.promptLine("Store URI", *uri)
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

	cfg.Stores[*name] = cloudstic.ProfileStore{
		URI:                     *uri,
		S3Region:                *s3Region,
		S3Profile:               *s3Profile,
		S3Endpoint:              *s3Endpoint,
		S3AccessKey:             *s3AccessKey,
		S3SecretKey:             *s3SecretKey,
		S3AccessKeyEnv:          "",
		S3SecretKeyEnv:          "",
		S3AccessKeySecret:       firstNonEmpty(*s3AccessKeySecret, envRef(*s3AccessKeyEnv)),
		S3SecretKeySecret:       firstNonEmpty(*s3SecretKeySecret, envRef(*s3SecretKeyEnv)),
		S3ProfileEnv:            *s3ProfileEnv,
		StoreSFTPPassword:       *sftpPassword,
		StoreSFTPKey:            *sftpKey,
		StoreSFTPPasswordEnv:    "",
		StoreSFTPKeyEnv:         "",
		StoreSFTPPasswordSecret: firstNonEmpty(*sftpPasswordSecret, envRef(*sftpPasswordEnv)),
		StoreSFTPKeySecret:      firstNonEmpty(*sftpKeySecret, envRef(*sftpKeyEnv)),
		PasswordEnv:             "",
		EncryptionKeyEnv:        "",
		RecoveryKeyEnv:          "",
		PasswordSecret:          firstNonEmpty(*passwordSecret, envRef(*passwordEnv)),
		EncryptionKeySecret:     firstNonEmpty(*encryptionKeySecret, envRef(*encryptionKeyEnv)),
		RecoveryKeySecret:       firstNonEmpty(*recoveryKeySecret, envRef(*recoveryKeyEnv)),
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
		if askKeepEncryption {
			keepCurrent, confirmErr := r.promptConfirm("Keep current encryption settings?", true)
			if confirmErr != nil {
				return r.fail("Failed to read encryption confirmation: %v", confirmErr)
			}
			forcePromptEncryption = !keepCurrent
		}
		if forcePromptEncryption || !storeHasExplicitEncryption(s) {
			r.promptEncryptionConfig(cfg, *name, *profilesFile)
		}
		if err := r.checkOrInitStore(cfg, *name, *profilesFile, true, !existedBefore, true); err != nil {
			_, _ = fmt.Fprintf(r.errOut, "%v\n", err)
		}
	}

	return 0
}

// checkOrInitStore connects to a store and checks if it is initialized.
// If already initialized, it confirms connectivity. If not, it offers to
// initialize it. Encryption config should already be saved in profiles.yaml
// before calling this. Errors are printed but never cause a non-zero exit—
// the store config has already been saved.
func (r *runner) runStoreVerify() int {
	fs := flag.NewFlagSet("store verify", flag.ExitOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))
	if fs.NArg() > 1 {
		return r.fail("usage: cloudstic store verify [-profiles-file <path>] <name>")
	}

	name := ""
	if fs.NArg() == 1 {
		name = fs.Arg(0)
	}

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if len(cfg.Stores) == 0 {
		return r.fail("No stores configured")
	}

	if name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic store verify [-profiles-file <path>] <name>")
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

	if _, ok := cfg.Stores[name]; !ok {
		return r.fail("Unknown store %q", name)
	}
	if err := r.checkOrInitStore(cfg, name, *profilesFile, false, true, false); err != nil {
		return r.fail("%v", err)
	}
	return 0
}

func (r *runner) checkOrInitStore(cfg *cloudstic.ProfilesConfig, storeName, profilesFile string, allowMissingSecrets, warnOnMissingSecrets, offerInit bool) error {
	s := cfg.Stores[storeName]
	g, err := globalFlagsFromProfileStore(s)
	if err != nil {
		if allowMissingSecrets && isSecretNotFoundError(err) {
			if warnOnMissingSecrets {
				_, _ = fmt.Fprintf(r.errOut, "Store credentials are configured but not currently available: %v\n", err)
				_, _ = fmt.Fprintf(r.errOut, "Set required secrets and run: cloudstic store verify %s\n", storeName)
			}
			return nil
		}
		return fmt.Errorf("could not resolve store credentials: %w", err)
	}
	raw, err := g.initObjectStore()
	if err != nil {
		return fmt.Errorf("could not connect to store: %w", err)
	}

	ctx := context.Background()

	// Check if already initialized by looking for the config marker.
	cfgData, err := raw.Get(ctx, "config")
	if err == nil && cfgData != nil {
		_, _ = fmt.Fprintln(r.out, "Store is already initialized and accessible.")
		repoCfg, cfgErr := cloudstic.LoadRepoConfig(ctx, raw)
		if cfgErr != nil {
			return fmt.Errorf("read repository config: %w", cfgErr)
		}
		if repoCfg != nil && repoCfg.Encrypted {
			_, _ = fmt.Fprintln(r.out, "Repository is encrypted; verifying configured credentials...")
			if err := verifyStoreEncryptionCredentials(ctx, g, raw); err != nil {
				return fmt.Errorf("store is initialized, but configured encryption credentials are invalid: %w", err)
			}
			_, _ = fmt.Fprintln(r.out, "Encryption credentials are valid.")
		}
		return nil
	}

	_, _ = fmt.Fprintln(r.out, "Store is accessible but not yet initialized.")
	if !offerInit {
		return nil
	}
	yes, promptErr := r.promptConfirm("Initialize it now?", true)
	if promptErr != nil || !yes {
		return nil
	}

	// Check if the store has encryption config.
	hasEncryption := storeHasExplicitEncryption(s)

	if !hasEncryption {
		// No encryption configured — init without encryption.
		result, initErr := cloudstic.InitRepo(ctx, raw, cloudstic.WithInitNoEncryption())
		if initErr != nil {
			return fmt.Errorf("init failed: %w", initErr)
		}
		r.printInitResult(result)
		return nil
	}

	// Build keychain from the store's encryption settings.
	// For password-based encryption, the env var must be set for init.
	// If not set, prompt for the password interactively.
	kc, err := g.buildKeychain(ctx)
	if err != nil {
		return fmt.Errorf("failed to build keychain: %w", err)
	}

	var initOpts []cloudstic.InitOption
	initOpts = append(initOpts, cloudstic.WithInitCredentials(kc))
	result, err := cloudstic.InitRepo(ctx, raw, initOpts...)
	if err != nil {
		return fmt.Errorf("init failed: %w", err)
	}
	r.printInitResult(result)
	return nil
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

	s, err := configureStoreEncryptionSelection(
		cfg.Stores[storeName],
		storeName,
		picked,
		r.promptSecretReference,
		r.promptLine,
		r.out,
	)
	if err != nil {
		_, _ = fmt.Fprintf(r.errOut, "%v\n", err)
		return
	}
	if picked == options[3] {
		return
	}

	// Save updated store config.
	cfg.Stores[storeName] = s
	if saveErr := cloudstic.SaveProfilesFile(profilesFile, cfg); saveErr != nil {
		_, _ = fmt.Fprintf(r.errOut, "Warning: could not save encryption settings: %v\n", saveErr)
	}
}

func configureStoreEncryptionSelection(
	s cloudstic.ProfileStore,
	storeName, picked string,
	promptSecretRef func(string, string, string, string) (string, error),
	promptLine func(string, string) (string, error),
	out io.Writer,
) (cloudstic.ProfileStore, error) {
	switch picked {
	case "Password (recommended for interactive use)":
		secretRef, err := promptSecretRef(storeName, "repository password", "CLOUDSTIC_PASSWORD", "password")
		if err != nil {
			return s, fmt.Errorf("failed to configure password secret: %w", err)
		}
		s.PasswordEnv = ""
		s.PasswordSecret = secretRef
		_, _ = fmt.Fprintf(out, "Encryption: password via %s\n", secretRef)
	case "Platform key (recommended for automation/CI)":
		secretRef, err := promptSecretRef(storeName, "platform key (64-char hex)", "CLOUDSTIC_ENCRYPTION_KEY", "encryption-key")
		if err != nil {
			return s, fmt.Errorf("failed to configure platform key secret: %w", err)
		}
		s.EncryptionKeyEnv = ""
		s.EncryptionKeySecret = secretRef
		_, _ = fmt.Fprintf(out, "Encryption: platform key via %s\n", secretRef)
	case "AWS KMS key (enterprise)":
		arn, err := promptLine("KMS key ARN", "")
		if err != nil || arn == "" {
			return s, fmt.Errorf("KMS key ARN is required")
		}
		s.KMSKeyARN = arn
		region, _ := promptLine("KMS region", "us-east-1")
		if region != "" {
			s.KMSRegion = region
		}
		_, _ = fmt.Fprintf(out, "Encryption: AWS KMS (%s)\n", arn)
	case "No encryption (not recommended)":
		_, _ = fmt.Fprintln(out, "Encryption: none (not recommended)")
	default:
		return s, fmt.Errorf("unsupported encryption selection: %s", picked)
	}
	return s, nil
}

func (r *runner) promptSecretReference(storeName, secretLabel, defaultEnvName, defaultAccount string) (string, error) {
	return promptSecretReferenceWithFns(
		runtime.GOOS,
		storeName,
		secretLabel,
		defaultEnvName,
		defaultAccount,
		r.promptSelect,
		r.promptLine,
		r.promptSecret,
		os.LookupEnv,
		nativeSecretExists,
		saveSecretToNativeStore,
	)
}

func promptSecretReferenceWithFns(
	goos, storeName, secretLabel, defaultEnvName, defaultAccount string,
	promptSelect func(string, []string) (string, error),
	promptLine func(string, string) (string, error),
	promptSecret func(string) (string, error),
	lookupEnv func(string) (string, bool),
	nativeSecretExists func(context.Context, string, string) (bool, error),
	writeNativeSecret func(context.Context, string, string, string) error,
) (string, error) {
	keychainRef := func() (string, error) {
		service := "cloudstic/store/" + storeName
		account := defaultAccount
		exists, err := nativeSecretExists(context.Background(), service, account)
		if err != nil {
			return "", err
		}
		if exists {
			return "keychain://" + service + "/" + account, nil
		}
		secretValue, err := promptSecret("Secret value")
		if err != nil {
			return "", err
		}
		if secretValue == "" {
			return "", fmt.Errorf("secret value cannot be empty")
		}
		if err := writeNativeSecret(context.Background(), service, account, secretValue); err != nil {
			return "", err
		}
		return "keychain://" + service + "/" + account, nil
	}

	if goos == "darwin" {
		picked, err := promptSelect(
			fmt.Sprintf("Where should %s be stored?", secretLabel),
			[]string{"Environment variable (env://)", "macOS Keychain (keychain://)"},
		)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(picked, "macOS Keychain") {
			return keychainRef()
		}
	}

	envName, err := promptLine("Env var name", defaultEnvName)
	if err != nil {
		return "", err
	}
	if _, ok := lookupEnv(envName); !ok && goos == "darwin" {
		picked, err := promptSelect(
			fmt.Sprintf("Environment variable %q is not set in this shell", envName),
			[]string{"Keep environment variable reference (env://)", "Store in macOS Keychain instead (keychain://)"},
		)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(picked, "Store in macOS Keychain") {
			return keychainRef()
		}
	}
	return envRef(envName), nil
}

func isSecretNotFoundError(err error) bool {
	var refErr *secretref.Error
	if errors.As(err, &refErr) {
		return refErr.Kind == secretref.KindNotFound
	}
	return false
}

func verifyStoreEncryptionCredentials(ctx context.Context, g *globalFlags, raw store.ObjectStore) error {
	kc, err := g.buildKeychain(ctx)
	if err != nil {
		return fmt.Errorf("build keychain: %w", err)
	}
	_, err = cloudstic.NewClient(raw,
		cloudstic.WithKeychain(kc),
		cloudstic.WithReporter(ui.NewNoOpReporter()),
	)
	if err != nil {
		return err
	}
	return nil
}

func hasStoreNewOverrideFlags(flagsSet map[string]bool) bool {
	for name := range flagsSet {
		switch name {
		case "name", "profiles-file":
			continue
		default:
			return true
		}
	}
	return false
}

func existingStoreInteractivePlan(canPrompt, hasOverrides, hasEncryption bool) (promptURI bool, askKeepEncryption bool) {
	if !canPrompt || hasOverrides {
		return false, false
	}
	return true, hasEncryption
}

// globalFlagsFromProfileStore builds a globalFlags populated from a ProfileStore,
// resolving env var indirections for secrets.
func globalFlagsFromProfileStore(s cloudstic.ProfileStore) (*globalFlags, error) {

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
	s3Profile, err := resolveProfileStoreValue("s3_profile", s.S3Profile, "", s.S3ProfileEnv)
	if err != nil {
		return nil, err
	}
	g.s3Profile = &s3Profile
	s3AccessKey, err := resolveProfileStoreValue("s3_access_key", s.S3AccessKey, s.S3AccessKeySecret, s.S3AccessKeyEnv)
	if err != nil {
		return nil, err
	}
	g.s3AccessKey = &s3AccessKey
	s3SecretKey, err := resolveProfileStoreValue("s3_secret_key", s.S3SecretKey, s.S3SecretKeySecret, s.S3SecretKeyEnv)
	if err != nil {
		return nil, err
	}
	g.s3SecretKey = &s3SecretKey
	storeSFTPPassword, err := resolveProfileStoreValue("store_sftp_password", s.StoreSFTPPassword, s.StoreSFTPPasswordSecret, s.StoreSFTPPasswordEnv)
	if err != nil {
		return nil, err
	}
	g.storeSFTPPassword = &storeSFTPPassword
	storeSFTPKey, err := resolveProfileStoreValue("store_sftp_key", s.StoreSFTPKey, s.StoreSFTPKeySecret, s.StoreSFTPKeyEnv)
	if err != nil {
		return nil, err
	}
	g.storeSFTPKey = &storeSFTPKey
	password, err := resolveProfileStoreValue("password", "", s.PasswordSecret, s.PasswordEnv)
	if err != nil {
		return nil, err
	}
	g.password = &password
	encryptionKey, err := resolveProfileStoreValue("encryption_key", "", s.EncryptionKeySecret, s.EncryptionKeyEnv)
	if err != nil {
		return nil, err
	}
	g.encryptionKey = &encryptionKey
	recoveryKey, err := resolveProfileStoreValue("recovery_key", "", s.RecoveryKeySecret, s.RecoveryKeyEnv)
	if err != nil {
		return nil, err
	}
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

	return g, nil
}

func envRef(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "env://" + name
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func storeHasExplicitEncryption(s cloudstic.ProfileStore) bool {
	return s.PasswordEnv != "" || s.EncryptionKeyEnv != "" ||
		s.RecoveryKeyEnv != "" || s.PasswordSecret != "" ||
		s.EncryptionKeySecret != "" || s.RecoveryKeySecret != "" ||
		s.KMSKeyARN != ""
}
