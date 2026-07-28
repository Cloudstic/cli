package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudstic/cli/pkg/config"
	"io"
	"os"
	"strings"

	"github.com/cloudstic/cli/pkg/profile"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/secretref"
	"github.com/cloudstic/cli/pkg/store"
)

type storeListArgs struct{ profilesFile string }

func declareStoreListArgs(g *globalFlags) (*storeListArgs, commandInput) {
	a := &storeListArgs{}
	return a, commandInput{flags: []flagSpec{profilesFileFlag(&a.profilesFile, g)}}
}

func runStoreList(r *runner, ctx context.Context, a *storeListArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	renderStoreList(r.out, cfg)
	return 0
}

type storeShowArgs struct {
	profilesFile string
	name         string
}

func declareStoreShowArgs(g *globalFlags) (*storeShowArgs, commandInput) {
	a := &storeShowArgs{}
	return a, commandInput{
		flags:       []flagSpec{profilesFileFlag(&a.profilesFile, g)},
		positionals: []positionalSpec{optionalPositional(&a.name, "store name", "")},
	}
}

func runStoreShow(r *runner, ctx context.Context, a *storeShowArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if a.name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic store show [-profiles-file <path>] <name>")
		}
		names := sortedKeys(cfg.Stores)
		picked, pickErr := r.promptSelect(ctx, "Select store", names)
		if pickErr != nil {
			return r.fail("Failed to select store: %v", pickErr)
		}
		a.name = picked
	}

	s, ok := cfg.Stores[a.name]
	if !ok {
		return r.fail("Unknown store %q", a.name)
	}
	renderStoreShow(r.out, cfg, a.name, s)
	return 0
}

type storeNewArgs struct {
	*globalFlags
	profilesFile                                           string
	name, uri                                              string
	s3Region, s3Profile, s3Endpoint                        string
	s3AccessKey, s3SecretKey                               string
	s3AccessKeySecret, s3SecretKeySecret                   string
	b2KeyID, b2AppKey                                      string
	b2KeyIDSecret, b2AppKeySecret                          string
	sftpPassword, sftpKey                                  string
	sftpPasswordSecret, sftpKeySecret                      string
	passwordSecret, encryptionKeySecret, recoveryKeySecret string
	kmsKeyARN, kmsRegion, kmsEndpoint                      string
}

func declareStoreNewArgs(g *globalFlags) (*storeNewArgs, commandInput) {
	a := &storeNewArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		profilesFileFlag(&a.profilesFile, g),
		stringFlag(&a.name, "name", "", "Store reference name", withPlaceholder("<name>")),
		stringFlag(&a.uri, "uri", "", "Store URI (e.g. s3:bucket/path, local:/path, sftp://host/path)", withPlaceholder("<uri>"), withCompleter("_cloudstic_store_prefixes")),
		stringFlag(&a.s3Region, "s3-region", "", "S3 region", withPlaceholder("<region>")),
		stringFlag(&a.s3Profile, "s3-profile", "", "AWS shared config profile", withPlaceholder("<name>")),
		stringFlag(&a.s3Endpoint, "s3-endpoint", "", "S3-compatible endpoint URL", withPlaceholder("<url>")),
		stringFlag(&a.s3AccessKey, "s3-access-key", "", "S3 static access key", withPlaceholder("<key>"), asSecret()),
		stringFlag(&a.s3SecretKey, "s3-secret-key", "", "S3 static secret key", withPlaceholder("<secret>"), asSecret()),
		stringFlag(&a.s3AccessKeySecret, "s3-access-key-secret", "", "Secret reference for S3 access key (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.s3SecretKeySecret, "s3-secret-key-secret", "", "Secret reference for S3 secret key (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.b2KeyID, "b2-key-id", "", "Backblaze B2 application key ID", withPlaceholder("<key-id>"), asSecret()),
		stringFlag(&a.b2AppKey, "b2-app-key", "", "Backblaze B2 application key", withPlaceholder("<key>"), asSecret()),
		stringFlag(&a.b2KeyIDSecret, "b2-key-id-secret", "", "Secret reference for B2 key ID (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.b2AppKeySecret, "b2-app-key-secret", "", "Secret reference for B2 application key (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.sftpPassword, "store-sftp-password", "", "SFTP password", withPlaceholder("<pass>"), asSecret()),
		stringFlag(&a.sftpKey, "store-sftp-key", "", "Path to SFTP private key", withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.sftpPasswordSecret, "store-sftp-password-secret", "", "Secret reference for SFTP password (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.sftpKeySecret, "store-sftp-key-secret", "", "Secret reference for SFTP key path (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.passwordSecret, "password-secret", "", "Secret reference for repository password (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.encryptionKeySecret, "encryption-key-secret", "", "Secret reference for platform key (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.recoveryKeySecret, "recovery-key-secret", "", "Secret reference for recovery key mnemonic (e.g. env://..., keychain://...)", withPlaceholder("<ref>")),
		stringFlag(&a.kmsKeyARN, "kms-key-arn", "", "AWS KMS key ARN", withPlaceholder("<arn>")),
		stringFlag(&a.kmsRegion, "kms-region", "", "AWS KMS region", withPlaceholder("<region>")),
		stringFlag(&a.kmsEndpoint, "kms-endpoint", "", "Custom AWS KMS endpoint URL", withPlaceholder("<url>")),
	}}
}

func (a *storeNewArgs) flagPtrs() storeNewFlagPtrs {
	return storeNewFlagPtrs{
		uri: &a.uri, s3Region: &a.s3Region, s3Profile: &a.s3Profile, s3Endpoint: &a.s3Endpoint,
		s3AccessKey: &a.s3AccessKey, s3SecretKey: &a.s3SecretKey,
		s3AccessKeySecret: &a.s3AccessKeySecret, s3SecretKeySecret: &a.s3SecretKeySecret,
		b2KeyID: &a.b2KeyID, b2AppKey: &a.b2AppKey,
		b2KeyIDSecret: &a.b2KeyIDSecret, b2AppKeySecret: &a.b2AppKeySecret,
		sftpPassword: &a.sftpPassword, sftpKey: &a.sftpKey,
		sftpPasswordSecret: &a.sftpPasswordSecret, sftpKeySecret: &a.sftpKeySecret,
		passwordSecret: &a.passwordSecret, encryptionKeySecret: &a.encryptionKeySecret,
		recoveryKeySecret: &a.recoveryKeySecret, kmsKeyARN: &a.kmsKeyARN,
		kmsRegion: &a.kmsRegion, kmsEndpoint: &a.kmsEndpoint,
	}
}

func runStoreNew(r *runner, ctx context.Context, a *storeNewArgs) int {
	storeFlags := a.flagPtrs()

	if a.name == "" {
		if r.canPrompt() {
			v, err := r.promptValidatedLine(ctx, "Store reference name", "", func(v string) error {
				if v == "" {
					return fmt.Errorf("store reference name is required")
				}
				return validateRefName("store", v)
			})
			if err != nil {
				return r.fail("Failed to read store name: %v", err)
			}
			a.name = v
		}
		if a.name == "" {
			return r.fail("-name is required")
		}
	}
	if err := validateRefName("store", a.name); err != nil {
		return r.fail("%v", err)
	}
	cfg, err := loadProfilesOrInit(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	ensureProfilesMaps(cfg)

	_, existedBefore := cfg.Stores[a.name]
	forcePromptURI := false
	forcePromptEncryption := false
	askKeepEncryption := false
	if existing, ok := cfg.Stores[a.name]; ok {
		applyExistingStoreDefaults(a.globalFlags, existing, storeFlags)
		if promptURI, askKeep := existingStoreInteractivePlan(r.canPrompt(), hasStoreNewOverrideFlags(a.globalFlags), storeHasExplicitEncryption(existing)); promptURI {
			forcePromptURI = true
			askKeepEncryption = askKeep
		}
	}

	if a.uri == "" || forcePromptURI {
		if r.canPrompt() {
			v, err := r.promptValidatedLine(ctx, "Store URI", a.uri, func(v string) error {
				if v == "" {
					return fmt.Errorf("store URI is required")
				}
				_, err := config.ParseStoreURI(v)
				return err
			})
			if err != nil {
				return r.fail("Failed to read store URI: %v", err)
			}
			a.uri = v
		}
		if a.uri == "" {
			return r.fail("-uri is required")
		}
	}

	// Validate the URI before saving.
	if _, err := config.ParseStoreURI(a.uri); err != nil {
		return r.fail("%v", err)
	}

	cfg.Stores[a.name] = buildProfileStoreFromFlags(storeFlags)

	if err := profile.Save(a.profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}
	_, _ = fmt.Fprintf(r.out, "Store %q saved in %s\n", a.name, a.profilesFile)

	if r.canPrompt() {
		// If no encryption flags were provided, prompt for encryption config.
		s := cfg.Stores[a.name]
		if askKeepEncryption {
			keepCurrent, confirmErr := r.promptConfirm(ctx, "Keep current encryption settings?", true)
			if confirmErr != nil {
				return r.fail("Failed to read encryption confirmation: %v", confirmErr)
			}
			forcePromptEncryption = !keepCurrent
		}
		if forcePromptEncryption || !storeHasExplicitEncryption(s) {
			r.promptEncryptionConfig(ctx, cfg, a.name, a.profilesFile, a.configDir)
		}
		if err := checkOrInitStoreWithRecovery(r, ctx, cfg, a.name, a.profilesFile, checkOrInitOptions{
			configDir:            a.configDir,
			allowMissingSecrets:  true,
			warnOnMissingSecrets: !existedBefore,
			offerInit:            true,
		}, true); err != nil {
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
type storeVerifyArgs struct {
	*globalFlags
	profilesFile string
	name         string
}

func declareStoreVerifyArgs(g *globalFlags) (*storeVerifyArgs, commandInput) {
	a := &storeVerifyArgs{globalFlags: g}
	return a, commandInput{
		flags:       []flagSpec{profilesFileFlag(&a.profilesFile, g)},
		positionals: []positionalSpec{optionalPositional(&a.name, "store name", "")},
	}
}

func runStoreVerify(r *runner, ctx context.Context, a *storeVerifyArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if len(cfg.Stores) == 0 {
		return r.fail("No stores configured")
	}

	if a.name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic store verify [-profiles-file <path>] <name>")
		}
		names := sortedKeys(cfg.Stores)
		picked, pickErr := r.promptSelect(ctx, "Select store", names)
		if pickErr != nil {
			return r.fail("Failed to select store: %v", pickErr)
		}
		a.name = picked
	}

	if _, ok := cfg.Stores[a.name]; !ok {
		return r.fail("Unknown store %q", a.name)
	}
	if err := checkOrInitStoreWithRecovery(r, ctx, cfg, a.name, a.profilesFile, checkOrInitOptions{
		configDir:            a.configDir,
		warnOnMissingSecrets: true,
	}, false); err != nil {
		return r.fail("%v", err)
	}
	return 0
}

type storeInitArgs struct {
	*globalFlags
	profilesFile string
	name         string
	yes          bool
}

func declareStoreInitArgs(g *globalFlags) (*storeInitArgs, commandInput) {
	a := &storeInitArgs{globalFlags: g}
	return a, commandInput{
		flags: []flagSpec{
			profilesFileFlag(&a.profilesFile, g),
			boolFlag(&a.yes, "yes", false, "Initialize without confirmation prompt"),
		},
		positionals: []positionalSpec{optionalPositional(&a.name, "store name", "")},
	}
}

func runStoreInit(r *runner, ctx context.Context, a *storeInitArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if len(cfg.Stores) == 0 {
		return r.fail("No stores configured")
	}

	if a.name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic store init [-profiles-file <path>] [-yes] <name>")
		}
		names := sortedKeys(cfg.Stores)
		picked, pickErr := r.promptSelect(ctx, "Select store", names)
		if pickErr != nil {
			return r.fail("Failed to select store: %v", pickErr)
		}
		a.name = picked
	}

	if _, ok := cfg.Stores[a.name]; !ok {
		return r.fail("Unknown store %q", a.name)
	}
	if err := checkOrInitStoreWithRecovery(r, ctx, cfg, a.name, a.profilesFile, checkOrInitOptions{
		configDir:            a.configDir,
		warnOnMissingSecrets: true,
		offerInit:            true,
		assumeYes:            a.yes,
	}, false); err != nil {
		return r.fail("%v", err)
	}
	return 0
}

type checkOrInitOptions struct {
	// configDir is the resolved -config-dir, which the store's secret
	// references are read relative to. See newSecretResolver.
	configDir            string
	allowMissingSecrets  bool
	warnOnMissingSecrets bool
	offerInit            bool
	assumeYes            bool
}

func checkOrInitStore(r *runner, ctx context.Context, cfg *profile.Config, storeName, profilesFile string, opts checkOrInitOptions) error {
	s := cfg.Stores[storeName]
	resolved, err := clientConfigFromProfileStore(s, opts.configDir)
	if err != nil {
		if opts.allowMissingSecrets && isSecretNotFoundError(err) {
			if opts.warnOnMissingSecrets {
				_, _ = fmt.Fprintf(r.errOut, "Store credentials are configured but not currently available: %v\n", err)
				_, _ = fmt.Fprintf(r.errOut, "Set required secrets and run: cloudstic store verify %s\n", storeName)
			}
			return nil
		}
		return fmt.Errorf("could not resolve store credentials: %w", err)
	}
	resolved.Unlock.NoPrompt = r.noPrompt
	raw, err := openStore(ctx, resolved.Store)
	if err != nil {
		return fmt.Errorf("could not connect to store: %w", err)
	}

	// Check if already initialized by looking for the config marker.
	cfgData, err := raw.Get(ctx, "config")
	if err == nil && cfgData != nil {
		_, _ = fmt.Fprintln(r.out, "Store is already initialized and accessible.")
		// InspectRepo, not LoadRepoConfig: an encrypted repository's marker is
		// sealed, and this runs before any credentials have been verified —
		// precisely to decide whether they need to be.
		repoStatus, cfgErr := cloudstic.InspectRepo(ctx, raw)
		if cfgErr != nil {
			return fmt.Errorf("read repository config: %w", cfgErr)
		}
		if repoStatus.Encrypted {
			_, _ = fmt.Fprintln(r.out, "Repository is encrypted; verifying configured credentials...")
			if err := verifyStoreEncryptionCredentials(ctx, resolved.Unlock, raw); err != nil {
				return fmt.Errorf("store is initialized, but configured encryption credentials are invalid: %w", err)
			}
			_, _ = fmt.Fprintln(r.out, "Encryption credentials are valid.")
		}
		return nil
	}

	_, _ = fmt.Fprintln(r.out, "Store is accessible but not yet initialized.")
	if !opts.offerInit {
		return nil
	}
	if !opts.assumeYes {
		yes, promptErr := r.promptConfirm(ctx, "Initialize it now?", true)
		if promptErr != nil || !yes {
			return nil
		}
	}

	// Check if the store has encryption config.
	hasEncryption := storeHasExplicitEncryption(s)

	if !hasEncryption {
		// No encryption configured — init without encryption.
		result, initErr := cloudstic.InitRepo(ctx, raw, cloudstic.WithInitNoEncryption())
		if initErr != nil {
			return fmt.Errorf("init failed: %w", initErr)
		}
		printInitResult(r.errOut, result)
		return nil
	}

	// Build keychain from the store's encryption settings.
	// For password-based encryption, the env var must be set for init.
	// If not set, prompt for the password interactively.
	kc, err := buildKeychain(ctx, resolved.Unlock)
	if err != nil {
		return fmt.Errorf("failed to build keychain: %w", err)
	}

	var initOpts []cloudstic.InitOption
	initOpts = append(initOpts, cloudstic.WithInitCredentials(kc))
	result, err := cloudstic.InitRepo(ctx, raw, initOpts...)
	if err != nil {
		return fmt.Errorf("init failed: %w", err)
	}
	printInitResult(r.errOut, result)
	return nil
}

func checkOrInitStoreWithRecovery(r *runner, ctx context.Context, cfg *profile.Config, storeName, profilesFile string, opts checkOrInitOptions, allowSkip bool) error {
	for {
		err := checkOrInitStore(r, ctx, cfg, storeName, profilesFile, opts)
		if err == nil || !r.canPrompt() {
			return err
		}

		s := cfg.Stores[storeName]
		options := []string{"Retry"}
		loginOption := awsSSOLoginOption(s, err)
		if loginOption != "" {
			options = append(options, loginOption)
		}
		if allowSkip {
			options = append(options, "Skip for now")
		} else {
			options = append(options, "Abort")
		}

		_, _ = fmt.Fprintf(r.errOut, "%v\n", err)
		picked, promptErr := r.promptSelect(ctx, "Store verification failed", options)
		if promptErr != nil {
			return err
		}

		switch picked {
		case "Retry":
			continue
		case "Skip for now":
			return nil
		case "Abort":
			return err
		case loginOption:
			if runErr := runAWSSSOLogin(r, ctx, s); runErr != nil {
				_, _ = fmt.Fprintf(r.errOut, "AWS SSO login failed: %v\n", runErr)
			}
		default:
			return err
		}
	}
}

// promptEncryptionConfig guides the user through encryption configuration
// and saves the chosen settings to profiles.yaml. It does not build a keychain
// or prompt for the actual password — that happens later during init.
func (r *runner) promptEncryptionConfig(ctx context.Context, cfg *profile.Config, storeName, profilesFile, configDir string) {
	_, _ = fmt.Fprintln(r.out)
	_, _ = fmt.Fprintln(r.out, "No encryption is configured for this store.")

	options := []string{
		"Password (recommended for interactive use)",
		"Platform key (recommended for automation/CI)",
		"AWS KMS key (enterprise)",
		"No encryption (not recommended)",
	}
	picked, err := r.promptSelect(ctx, "Select encryption method", options)
	if err != nil {
		_, _ = fmt.Fprintf(r.errOut, "Failed to select encryption method: %v\n", err)
		return
	}

	s, err := configureStoreEncryptionSelection(
		ctx,
		cfg.Stores[storeName],
		storeName,
		picked,
		func(ctx context.Context, storeName, secretLabel, defaultEnvName, defaultAccount string) (string, error) {
			return r.promptSecretReference(ctx, configDir, storeName, secretLabel, defaultEnvName, defaultAccount)
		},
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
	if saveErr := profile.Save(profilesFile, cfg); saveErr != nil {
		_, _ = fmt.Fprintf(r.errOut, "Warning: could not save encryption settings: %v\n", saveErr)
	}
}

func configureStoreEncryptionSelection(
	ctx context.Context,
	s profile.Store,
	storeName, picked string,
	promptSecretRef func(context.Context, string, string, string, string) (string, error),
	promptLine func(context.Context, string, string) (string, error),
	out io.Writer,
) (profile.Store, error) {
	switch picked {
	case "Password (recommended for interactive use)":
		secretRef, err := promptSecretRef(ctx, storeName, "repository password", "CLOUDSTIC_PASSWORD", "password")
		if err != nil {
			return s, fmt.Errorf("failed to configure password secret: %w", err)
		}
		s.PasswordSecret = secretRef
		_, _ = fmt.Fprintf(out, "Encryption: password via %s\n", secretRef)
	case "Platform key (recommended for automation/CI)":
		secretRef, err := promptSecretRef(ctx, storeName, "platform key (64-char hex)", "CLOUDSTIC_ENCRYPTION_KEY", "encryption-key")
		if err != nil {
			return s, fmt.Errorf("failed to configure platform key secret: %w", err)
		}
		s.EncryptionKeySecret = secretRef
		_, _ = fmt.Fprintf(out, "Encryption: platform key via %s\n", secretRef)
	case "AWS KMS key (enterprise)":
		arn, err := promptLine(ctx, "KMS key ARN", "")
		if err != nil || arn == "" {
			return s, fmt.Errorf("KMS key ARN is required")
		}
		s.KMSKeyARN = arn
		region, _ := promptLine(ctx, "KMS region", "us-east-1")
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

func (r *runner) promptSecretReference(ctx context.Context, configDir, storeName, secretLabel, defaultEnvName, defaultAccount string) (string, error) {
	return promptSecretReferenceWithFns(
		ctx,
		storeName,
		secretLabel,
		defaultEnvName,
		defaultAccount,
		func(_ context.Context, l string, o []string) (string, error) { return r.promptSelect(ctx, l, o) },
		func(ctx context.Context, l, d string) (string, error) { return r.promptLine(ctx, l, d) },
		func(_ context.Context, s string) (string, error) { return r.promptSecret(ctx, s) },
		os.LookupEnv,
		newSecretResolver(configDir),
	)
}

func promptSecretReferenceWithFns(
	ctx context.Context,
	storeName, secretLabel, defaultEnvName, defaultAccount string,
	promptSelect func(context.Context, string, []string) (string, error),
	promptLine func(context.Context, string, string) (string, error),
	promptSecret func(context.Context, string) (string, error),
	lookupEnv func(string) (string, bool),
	resolver *secretref.Resolver,
) (string, error) {
	writableBackends := resolver.WritableBackends()
	nativeRef := func(backend secretref.WritableBackend) (string, error) {
		ref := backend.DefaultRef(storeName, defaultAccount)
		exists, err := resolver.Exists(ctx, ref)
		if err != nil {
			return "", err
		}
		if exists {
			return ref, nil
		}
		secretValue, err := promptSecret(ctx, "Secret value")
		if err != nil {
			return "", err
		}
		if secretValue == "" {
			return "", fmt.Errorf("secret value cannot be empty")
		}
		if err := resolver.Store(ctx, ref, secretValue); err != nil {
			return "", err
		}
		return ref, nil
	}

	if len(writableBackends) > 0 {
		options := []string{"Environment variable (env://)"}
		backendByOption := map[string]secretref.WritableBackend{}
		for _, backend := range writableBackends {
			option := fmt.Sprintf("%s (%s://)", backend.DisplayName(), backend.Scheme())
			options = append(options, option)
			backendByOption[option] = backend
		}
		picked, err := promptSelect(
			ctx,
			fmt.Sprintf("Where should %s be stored?", secretLabel),
			options,
		)
		if err != nil {
			return "", err
		}
		if backend, ok := backendByOption[picked]; ok {
			return nativeRef(backend)
		}
	}

	envName, err := promptLine(ctx, "Env var name", defaultEnvName)
	if err != nil {
		return "", err
	}
	if _, ok := lookupEnv(envName); !ok && len(writableBackends) > 0 {
		options := []string{"Keep environment variable reference (env://)"}
		backendByOption := map[string]secretref.WritableBackend{}
		for _, backend := range writableBackends {
			option := fmt.Sprintf("Store in %s instead (%s://)", backend.DisplayName(), backend.Scheme())
			options = append(options, option)
			backendByOption[option] = backend
		}
		picked, err := promptSelect(
			ctx,
			fmt.Sprintf("Environment variable %q is not set in this shell", envName),
			options,
		)
		if err != nil {
			return "", err
		}
		if backend, ok := backendByOption[picked]; ok {
			return nativeRef(backend)
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

func verifyStoreEncryptionCredentials(ctx context.Context, cfg unlockConfig, raw store.ObjectStore) error {
	kc, err := buildKeychain(ctx, cfg)
	if err != nil {
		return fmt.Errorf("build keychain: %w", err)
	}
	_, err = cloudstic.NewClient(ctx, raw,
		cloudstic.WithKeychain(kc),
		cloudstic.WithReporter(ui.NewNoOpReporter()),
	)
	if err != nil {
		return err
	}
	return nil
}

func awsSSOLoginOption(s profile.Store, err error) string {
	if !isAWSExpiredAuthError(err) {
		return ""
	}
	if s.S3Profile != "" {
		return fmt.Sprintf("Run aws sso login --profile %s", s.S3Profile)
	}
	return "Run aws sso login"
}

func isAWSExpiredAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"sso session has expired",
		"sso session is expired",
		"sso session is invalid",
		"ssoproviderinvalidtoken",
		"token has expired and refresh failed",
		"the security token included in the request is expired",
		"expiredtoken",
		"expired token",
		"invalid security token",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func runAWSSSOLogin(r *runner, ctx context.Context, s profile.Store) error {
	args := []string{"sso", "login"}
	if s.S3Profile != "" {
		args = append(args, "--profile", s.S3Profile)
	}
	_, _ = fmt.Fprintf(r.errOut, "Running: aws %s\n", strings.Join(args, " "))
	runFn := r.runInteractiveCmd
	if runFn == nil {
		runFn = defaultRunInteractiveCmd
	}
	return runFn(ctx, r.stdin, r.out, r.errOut, "aws", args...)
}

func hasStoreNewOverrideFlags(g *globalFlags) bool {
	for name, origin := range g.origins {
		if origin != originFlag {
			continue
		}
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

func envRef(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "env://" + name
}

func storeHasExplicitEncryption(s profile.Store) bool {
	return s.PasswordSecret != "" || s.EncryptionKeySecret != "" || s.RecoveryKeySecret != "" ||
		s.KMSKeyARN != ""
}

// storeCommand declares the `store` command group.
func storeCommand() command {
	return group("store", "Manage store entries in profiles.yaml",
		leaf("new", "Create or update a store entry in profiles.yaml", nil, declareStoreNewArgs, runStoreNew),
		leaf("list", "List configured stores", nil, declareStoreListArgs, runStoreList),
		leaf("show", "Show one store and its configuration", nil, declareStoreShowArgs, runStoreShow),
		leaf("verify", "Verify one store's credentials and connectivity", nil, declareStoreVerifyArgs, runStoreVerify),
		leaf("init", "Initialize a configured store by reference", nil, declareStoreInitArgs, runStoreInit),
	)
}
