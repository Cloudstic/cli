package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/cloudstic/cli/pkg/config"

	"github.com/cloudstic/cli/pkg/profile"
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
	cfg, err := profile.LoadOrEmpty(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	profile.EnsureMaps(cfg)

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
			promptEncryptionConfig(r, ctx, cfg, a.name, a.profilesFile, a.configDir)
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
