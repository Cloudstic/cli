package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/cloudstic/cli/internal/onboarding"
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
	profilesFile string
	name         string
	// values holds one target per store-field flag, keyed by the flag's name.
	//
	// A named struct field per flag would be a second statement of the field
	// set — which is what `storeNewFlagPtrs` was, along with the two functions
	// that walked it. Keying by flag name lets the profile entry be assembled
	// from config.StoreFieldSpecs() instead, so a field added to the profiles
	// format reaches `store new` without an edit here (issue #568).
	values map[string]*string
}

// field declares one store-field flag and registers its target under the flag's
// own name. The usage text stays written out per flag: it is prose for a human
// reading `-h`, and the generic label on the table ("S3 Access Key") is not a
// substitute for "S3 static access key".
func (a *storeNewArgs) field(name, usage string, opts ...flagOpt) flagSpec {
	target := new(string)
	a.values[name] = target
	return stringFlag(target, name, "", usage, opts...)
}

// secretRefUsage is the shared wording for the flags that name where a
// credential lives rather than carrying it.
func secretRefUsage(what string) string {
	return "Secret reference for " + what + " (e.g. env://..., keychain://...)"
}

func declareStoreNewArgs(g *globalFlags) (*storeNewArgs, commandInput) {
	a := &storeNewArgs{globalFlags: g, values: map[string]*string{}}
	return a, commandInput{flags: []flagSpec{
		profilesFileFlag(&a.profilesFile, g),
		stringFlag(&a.name, "name", "", "Store reference name", withPlaceholder("<name>")),
		a.field("uri", "Store URI (e.g. s3:bucket/path, local:/path, sftp://host/path)", withPlaceholder("<uri>"), withCompleter("_cloudstic_store_prefixes")),
		a.field("s3-region", "S3 region", withPlaceholder("<region>")),
		a.field("s3-profile", "AWS shared config profile", withPlaceholder("<name>")),
		a.field("s3-endpoint", "S3-compatible endpoint URL", withPlaceholder("<url>")),
		a.field("s3-access-key", "S3 static access key", withPlaceholder("<key>"), asSecret()),
		a.field("s3-secret-key", "S3 static secret key", withPlaceholder("<secret>"), asSecret()),
		a.field("s3-access-key-secret", secretRefUsage("S3 access key"), withPlaceholder("<ref>")),
		a.field("s3-secret-key-secret", secretRefUsage("S3 secret key"), withPlaceholder("<ref>")),
		a.field("b2-key-id", "Backblaze B2 application key ID", withPlaceholder("<key-id>"), asSecret()),
		a.field("b2-app-key", "Backblaze B2 application key", withPlaceholder("<key>"), asSecret()),
		a.field("b2-key-id-secret", secretRefUsage("B2 key ID"), withPlaceholder("<ref>")),
		a.field("b2-app-key-secret", secretRefUsage("B2 application key"), withPlaceholder("<ref>")),
		a.field("store-sftp-password", "SFTP password", withPlaceholder("<pass>"), asSecret()),
		a.field("store-sftp-key", "Path to SFTP private key", withPlaceholder("<path>"), withCompleter("_files")),
		a.field("store-sftp-password-secret", secretRefUsage("SFTP password"), withPlaceholder("<ref>")),
		a.field("store-sftp-key-secret", secretRefUsage("SFTP key path"), withPlaceholder("<ref>")),
		a.field("password-secret", secretRefUsage("repository password"), withPlaceholder("<ref>")),
		a.field("encryption-key-secret", secretRefUsage("platform key"), withPlaceholder("<ref>")),
		a.field("recovery-key-secret", secretRefUsage("recovery key mnemonic"), withPlaceholder("<ref>")),
		a.field("kms-key-arn", "AWS KMS key ARN", withPlaceholder("<arn>")),
		a.field("kms-region", "AWS KMS region", withPlaceholder("<region>")),
		a.field("kms-endpoint", "Custom AWS KMS endpoint URL", withPlaceholder("<url>")),
	}}
}

// toProfileStore assembles the entry these flags describe, reading the field
// set from pkg/config rather than restating it.
func (a *storeNewArgs) toProfileStore() profile.Store {
	var s profile.Store
	for _, f := range config.StoreFieldSpecs() {
		if name := f.FlagName(); name != "" {
			f.SetInline(&s, a.value(name))
		}
		if name := f.SecretFlagName(); name != "" {
			f.SetRef(&s, a.value(name))
		}
	}
	return s
}

func (a *storeNewArgs) value(flagName string) string {
	if p := a.values[flagName]; p != nil {
		return *p
	}
	return ""
}

func runStoreNew(r *runner, ctx context.Context, a *storeNewArgs) int {
	name, err := onboarding.Resolve(ctx, prompterFor(r), a.name, onboarding.Field{
		Label:    "Store reference name",
		Noun:     "store reference name",
		Missing:  "-name is required",
		Validate: func(v string) error { return onboarding.ValidateRefName("store", v) },
	})
	if err != nil {
		return r.fail("%v", err)
	}
	a.name = name
	cfg, err := profile.LoadOrEmpty(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	profile.EnsureMaps(cfg)

	existing, existedBefore := cfg.Stores[a.name]
	forcePromptURI := false
	forcePromptEncryption := false
	askKeepEncryption := false

	// Fold the flags over the stored entry once, here, rather than copying the
	// entry back into the flag targets first. Every field the user did not pass
	// keeps what the file held, which is what makes `store new <existing>` a
	// way to change one setting.
	store := config.MergeStoreInto(existing, a.toProfileStore(), a.flagProvided)

	if existedBefore {
		if promptURI, askKeep := existingStoreInteractivePlan(r.canPrompt(), hasStoreNewOverrideFlags(a.globalFlags), storeHasExplicitEncryption(existing)); promptURI {
			forcePromptURI = true
			askKeepEncryption = askKeep
		}
	}

	if store.URI == "" || forcePromptURI {
		if r.canPrompt() {
			v, err := r.promptValidatedLine(ctx, "Store URI", store.URI, func(v string) error {
				if v == "" {
					return fmt.Errorf("store URI is required")
				}
				_, err := config.ParseStoreURI(v)
				return err
			})
			if err != nil {
				return r.fail("Failed to read store URI: %v", err)
			}
			store.URI = v
		}
		if store.URI == "" {
			return r.fail("-uri is required")
		}
	}

	// Validate the URI before saving.
	//
	// Fields belonging to other backends are deliberately *not* cleared here.
	// `store new` accepts credentials for a backend the URI does not name —
	// TestRunStoreNew_WithSecretRefFlags sets SFTP references on an s3: store —
	// and dropping them would be a behaviour change this refactor has no
	// mandate for. The TUI's SaveStore does clear them, because a form
	// round-trips every field and would otherwise resurrect stale ones.
	if _, err := config.ParseStoreURI(store.URI); err != nil {
		return r.fail("%v", err)
	}

	cfg.Stores[a.name] = store

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
