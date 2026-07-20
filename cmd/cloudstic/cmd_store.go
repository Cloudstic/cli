package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/secretref"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

func runStore(r *runner, ctx context.Context) int {
	if len(r.args) < 1 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic store <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: list, show, new, verify, init")
		return 1
	}

	subcommand := r.args[0]
	subRunner := r.withArgs(r.args[1:])
	switch subcommand {
	case "list":
		return runStoreList(subRunner, ctx)
	case "show":
		return runStoreShow(subRunner, ctx)
	case "new":
		return runStoreNew(subRunner, ctx)
	case "verify":
		return runStoreVerify(subRunner, ctx)
	case "init":
		return runStoreInit(subRunner, ctx)
	default:
		return r.fail("Unknown store subcommand: %s", subcommand)
	}
}

func runStoreList(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("store list", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
		return r.parseError(err)
	}

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	renderStoreList(r.out, cfg)
	return 0
}

func runStoreShow(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("store show", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
		return r.parseError(err)
	}
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
		names := sortedKeys(cfg.Stores)
		picked, pickErr := r.promptSelect(ctx, "Select store", names)
		if pickErr != nil {
			return r.fail("Failed to select store: %v", pickErr)
		}
		name = picked
	}

	s, ok := cfg.Stores[name]
	if !ok {
		return r.fail("Unknown store %q", name)
	}
	renderStoreShow(r.out, cfg, name, s)
	return 0
}

func runStoreNew(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("store new", flag.ContinueOnError)
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
	sftpPassword := fs.String("store-sftp-password", "", "SFTP password")
	sftpKey := fs.String("store-sftp-key", "", "Path to SFTP private key")
	sftpPasswordSecret := fs.String("store-sftp-password-secret", "", "Secret reference for SFTP password (e.g. env://..., keychain://...)")
	sftpKeySecret := fs.String("store-sftp-key-secret", "", "Secret reference for SFTP key path (e.g. env://..., keychain://...)")
	passwordSecret := fs.String("password-secret", "", "Secret reference for repository password (e.g. env://..., keychain://...)")
	encryptionKeySecret := fs.String("encryption-key-secret", "", "Secret reference for platform key (e.g. env://..., keychain://...)")
	recoveryKeySecret := fs.String("recovery-key-secret", "", "Secret reference for recovery key mnemonic (e.g. env://..., keychain://...)")
	kmsKeyARN := fs.String("kms-key-arn", "", "AWS KMS key ARN")
	kmsRegion := fs.String("kms-region", "", "AWS KMS region")
	kmsEndpoint := fs.String("kms-endpoint", "", "Custom AWS KMS endpoint URL")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
		return r.parseError(err)
	}

	flagsSet := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { flagsSet[f.Name] = true })
	storeFlags := storeNewFlagPtrs{
		uri:                 uri,
		s3Region:            s3Region,
		s3Profile:           s3Profile,
		s3Endpoint:          s3Endpoint,
		s3AccessKey:         s3AccessKey,
		s3SecretKey:         s3SecretKey,
		s3AccessKeySecret:   s3AccessKeySecret,
		s3SecretKeySecret:   s3SecretKeySecret,
		sftpPassword:        sftpPassword,
		sftpKey:             sftpKey,
		sftpPasswordSecret:  sftpPasswordSecret,
		sftpKeySecret:       sftpKeySecret,
		passwordSecret:      passwordSecret,
		encryptionKeySecret: encryptionKeySecret,
		recoveryKeySecret:   recoveryKeySecret,
		kmsKeyARN:           kmsKeyARN,
		kmsRegion:           kmsRegion,
		kmsEndpoint:         kmsEndpoint,
	}

	if *name == "" {
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
			*name = v
		}
		if *name == "" {
			return r.fail("-name is required")
		}
	}
	if err := validateRefName("store", *name); err != nil {
		return r.fail("%v", err)
	}
	cfg, err := loadProfilesOrInit(*profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	ensureProfilesMaps(cfg)

	_, existedBefore := cfg.Stores[*name]
	forcePromptURI := false
	forcePromptEncryption := false
	askKeepEncryption := false
	if existing, ok := cfg.Stores[*name]; ok {
		applyExistingStoreDefaults(flagsSet, existing, storeFlags)
		if promptURI, askKeep := existingStoreInteractivePlan(r.canPrompt(), hasStoreNewOverrideFlags(flagsSet), storeHasExplicitEncryption(existing)); promptURI {
			forcePromptURI = true
			askKeepEncryption = askKeep
		}
	}

	if *uri == "" || forcePromptURI {
		if r.canPrompt() {
			v, err := r.promptValidatedLine(ctx, "Store URI", *uri, func(v string) error {
				if v == "" {
					return fmt.Errorf("store URI is required")
				}
				_, err := parseStoreURI(v)
				return err
			})
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

	cfg.Stores[*name] = buildProfileStoreFromFlags(storeFlags)

	if err := cloudstic.SaveProfilesFile(*profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}
	_, _ = fmt.Fprintf(r.out, "Store %q saved in %s\n", *name, *profilesFile)

	if r.canPrompt() {
		// If no encryption flags were provided, prompt for encryption config.
		s := cfg.Stores[*name]
		if askKeepEncryption {
			keepCurrent, confirmErr := r.promptConfirm(ctx, "Keep current encryption settings?", true)
			if confirmErr != nil {
				return r.fail("Failed to read encryption confirmation: %v", confirmErr)
			}
			forcePromptEncryption = !keepCurrent
		}
		if forcePromptEncryption || !storeHasExplicitEncryption(s) {
			r.promptEncryptionConfig(ctx, cfg, *name, *profilesFile)
		}
		if err := checkOrInitStoreWithRecovery(r, ctx, cfg, *name, *profilesFile, checkOrInitOptions{
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
func runStoreVerify(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("store verify", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
		return r.parseError(err)
	}
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
		names := sortedKeys(cfg.Stores)
		picked, pickErr := r.promptSelect(ctx, "Select store", names)
		if pickErr != nil {
			return r.fail("Failed to select store: %v", pickErr)
		}
		name = picked
	}

	if _, ok := cfg.Stores[name]; !ok {
		return r.fail("Unknown store %q", name)
	}
	if err := checkOrInitStoreWithRecovery(r, ctx, cfg, name, *profilesFile, checkOrInitOptions{
		warnOnMissingSecrets: true,
	}, false); err != nil {
		return r.fail("%v", err)
	}
	return 0
}

func runStoreInit(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("store init", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	yes := fs.Bool("yes", false, "Initialize without confirmation prompt")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
		return r.parseError(err)
	}
	if fs.NArg() > 1 {
		return r.fail("usage: cloudstic store init [-profiles-file <path>] [-yes] <name>")
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
			return r.fail("usage: cloudstic store init [-profiles-file <path>] [-yes] <name>")
		}
		names := sortedKeys(cfg.Stores)
		picked, pickErr := r.promptSelect(ctx, "Select store", names)
		if pickErr != nil {
			return r.fail("Failed to select store: %v", pickErr)
		}
		name = picked
	}

	if _, ok := cfg.Stores[name]; !ok {
		return r.fail("Unknown store %q", name)
	}
	if err := checkOrInitStoreWithRecovery(r, ctx, cfg, name, *profilesFile, checkOrInitOptions{
		warnOnMissingSecrets: true,
		offerInit:            true,
		assumeYes:            *yes,
	}, false); err != nil {
		return r.fail("%v", err)
	}
	return 0
}

type checkOrInitOptions struct {
	allowMissingSecrets  bool
	warnOnMissingSecrets bool
	offerInit            bool
	assumeYes            bool
}

func checkOrInitStore(r *runner, ctx context.Context, cfg *cloudstic.ProfilesConfig, storeName, profilesFile string, opts checkOrInitOptions) error {
	s := cfg.Stores[storeName]
	g, err := globalFlagsFromProfileStore(s)
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
	g.noPrompt = r.noPrompt
	raw, err := g.initObjectStore(ctx)
	if err != nil {
		return fmt.Errorf("could not connect to store: %w", err)
	}

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
	printInitResult(r.errOut, result)
	return nil
}

func checkOrInitStoreWithRecovery(r *runner, ctx context.Context, cfg *cloudstic.ProfilesConfig, storeName, profilesFile string, opts checkOrInitOptions, allowSkip bool) error {
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
func (r *runner) promptEncryptionConfig(ctx context.Context, cfg *cloudstic.ProfilesConfig, storeName, profilesFile string) {
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
	ctx context.Context,
	s cloudstic.ProfileStore,
	storeName, picked string,
	promptSecretRef func(context.Context, string, string, string, string) (string, error),
	promptLine func(context.Context, string, string) (string, error),
	out io.Writer,
) (cloudstic.ProfileStore, error) {
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

func (r *runner) promptSecretReference(ctx context.Context, storeName, secretLabel, defaultEnvName, defaultAccount string) (string, error) {
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
		profileSecretResolver,
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

func verifyStoreEncryptionCredentials(ctx context.Context, g *globalFlags, raw store.ObjectStore) error {
	kc, err := g.buildKeychain(ctx)
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

func awsSSOLoginOption(s cloudstic.ProfileStore, err error) string {
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

func runAWSSSOLogin(r *runner, ctx context.Context, s cloudstic.ProfileStore) error {
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
	g.store = s.URI
	g.s3Endpoint = s.S3Endpoint
	g.s3Region = s.S3Region
	if g.s3Region == "" {
		g.s3Region = "us-east-1"
	}

	var err error
	if g.s3Profile, err = resolveProfileStoreValue("s3_profile", s.S3Profile, ""); err != nil {
		return nil, err
	}
	if g.s3AccessKey, err = resolveProfileStoreValue("s3_access_key", s.S3AccessKey, s.S3AccessKeySecret); err != nil {
		return nil, err
	}
	if g.s3SecretKey, err = resolveProfileStoreValue("s3_secret_key", s.S3SecretKey, s.S3SecretKeySecret); err != nil {
		return nil, err
	}
	if g.storeSFTPPassword, err = resolveProfileStoreValue("store_sftp_password", s.StoreSFTPPassword, s.StoreSFTPPasswordSecret); err != nil {
		return nil, err
	}
	if g.storeSFTPKey, err = resolveProfileStoreValue("store_sftp_key", s.StoreSFTPKey, s.StoreSFTPKeySecret); err != nil {
		return nil, err
	}
	if g.password, err = resolveProfileStoreValue("password", "", s.PasswordSecret); err != nil {
		return nil, err
	}
	if g.encryptionKey, err = resolveProfileStoreValue("encryption_key", "", s.EncryptionKeySecret); err != nil {
		return nil, err
	}
	if g.recoveryKey, err = resolveProfileStoreValue("recovery_key", "", s.RecoveryKeySecret); err != nil {
		return nil, err
	}
	g.kmsKeyARN = s.KMSKeyARN
	g.kmsRegion = s.KMSRegion
	g.kmsEndpoint = s.KMSEndpoint

	// Non-store fields with safe defaults: ProfileStore carries no source-side
	// SFTP credentials or SFTP host-key-verification overrides, so those stay
	// at their zero value (disabled/empty) rather than left unset.
	g.sourceSFTPPassword = ""
	g.sourceSFTPKey = ""
	g.sourceSFTPInsecure = false
	g.sourceSFTPKnownHosts = ""
	g.storeSFTPInsecure = false
	g.storeSFTPKnownHosts = ""
	g.disablePackfile = false
	g.prompt = false
	g.verbose = false
	g.quiet = false
	g.debug = false
	g.profile = ""
	g.profilesFile = ""

	return g, nil
}

func envRef(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "env://" + name
}

func storeHasExplicitEncryption(s cloudstic.ProfileStore) bool {
	return s.PasswordSecret != "" || s.EncryptionKeySecret != "" || s.RecoveryKeySecret != "" ||
		s.KMSKeyARN != ""
}
