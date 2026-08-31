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

const defaultProfilesFilename = profile.DefaultFilename

// defaultProfilesPath returns where the profiles file lives when the user
// names no path.
//
// The rule itself lives in pkg/profile, so a library caller reaching for a
// user's profiles lands on the same file this CLI does (RFC 0022 §7); this
// wrapper exists only to keep the call sites reading in flag terms.
func defaultProfilesPath(configDir string) (string, error) {
	return profile.DefaultPath(configDir)
}

type profileShowArgs struct {
	profilesFile string
	name         string
}

func declareProfileShowArgs(g *globalFlags) (*profileShowArgs, commandInput) {
	a := &profileShowArgs{}
	return a, commandInput{
		flags:       []flagSpec{profilesFileFlag(&a.profilesFile, g)},
		positionals: []positionalSpec{optionalPositional(&a.name, "profile name", "", "_cloudstic_profile_names")},
	}
}

func runProfileShow(r *runner, ctx context.Context, a *profileShowArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if a.name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic profile show [-profiles-file <path>] <name>")
		}
		names := sortedKeys(cfg.Profiles)
		picked, pickErr := r.promptSelect(ctx, "Select profile", names)
		if pickErr != nil {
			return r.fail("Failed to select profile: %v", pickErr)
		}
		a.name = picked
	}
	p, ok := cfg.Profiles[a.name]
	if !ok {
		return r.fail("Unknown profile %q", a.name)
	}
	renderProfileShow(r.out, cfg, a.name, p)
	return 0
}

func profileStoreAuthMode(s profile.Store) string {
	if s.S3AccessKey != "" || s.S3SecretKey != "" || s.S3AccessKeySecret != "" || s.S3SecretKeySecret != "" {
		return "static-keys"
	}
	if s.S3Profile != "" {
		return "aws-shared-profile"
	}
	if s.B2KeyID != "" || s.B2AppKey != "" || s.B2KeyIDSecret != "" || s.B2AppKeySecret != "" {
		return "b2-keys"
	}
	if s.StoreSFTPPassword != "" || s.StoreSFTPKey != "" || s.StoreSFTPPasswordSecret != "" || s.StoreSFTPKeySecret != "" {
		return "sftp"
	}
	return "default-chain"
}

type profileListArgs struct {
	profilesFile string
}

func declareProfileListArgs(g *globalFlags) (*profileListArgs, commandInput) {
	a := &profileListArgs{}
	return a, commandInput{flags: []flagSpec{profilesFileFlag(&a.profilesFile, g)}}
}

func runProfileList(r *runner, ctx context.Context, a *profileListArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	renderStoreList(r.out, cfg)
	_, _ = fmt.Fprintln(r.out)
	renderAuthList(r.out, cfg)
	_, _ = fmt.Fprintln(r.out)
	renderProfileList(r.out, cfg)

	return 0
}

type profileNewArgs struct {
	*globalFlags
	profilesFile      string
	name              string
	source            string
	storeRef          string
	store             string
	authRef           string
	tags              stringArrayFlags
	excludes          stringArrayFlags
	excludeFile       string
	ignoreEmpty       bool
	skipNativeFiles   bool
	volumeUUID        string
	googleCreds       string
	googleCredsRef    string
	googleCredsJSON   string
	googleTokenFile   string
	googleTokenRef    string
	onedriveClientID  string
	onedriveTokenFile string
	onedriveTokenRef  string
}

func declareProfileNewArgs(g *globalFlags) (*profileNewArgs, commandInput) {
	a := &profileNewArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		profilesFileFlag(&a.profilesFile, g),
		stringFlag(&a.name, "name", "", "Profile name", withPlaceholder("<name>")),
		stringFlag(&a.source, "source", "", "Source URI", withPlaceholder("<uri>"), withCompleter("_cloudstic_source_prefixes")),
		stringFlag(&a.storeRef, "store-ref", "", "Store reference name from top-level stores map", withPlaceholder("<name>")),
		stringFlag(&a.store, "store", "", "Store URI to create/update under -store-ref", withPlaceholder("<uri>"), withCompleter("_cloudstic_store_prefixes")),
		stringFlag(&a.authRef, "auth-ref", "", "Auth reference name from top-level auth map", withPlaceholder("<name>"), withCompleter("_cloudstic_auth_names")),
		valueFlag(&a.tags, "tag", "Tag to apply to snapshots (repeatable)", withPlaceholder("<tag>"), asRepeatable()),
		valueFlag(&a.excludes, "exclude", "Exclude pattern (repeatable)", withPlaceholder("<pattern>"), asRepeatable()),
		stringFlag(&a.excludeFile, "exclude-file", "", "Path to file with exclude patterns", withPlaceholder("<path>"), withCompleter("_files")),
		boolFlag(&a.ignoreEmpty, "ignore-empty-snapshot", false, "Skip creating a new snapshot when nothing changed"),
		boolFlag(&a.skipNativeFiles, "skip-native-files", false, "Exclude Google-native files (Docs, Sheets, Slides, etc.)"),
		stringFlag(&a.volumeUUID, "volume-uuid", "", "Override volume UUID for local source", withPlaceholder("<uuid>")),
		stringFlag(&a.googleCreds, "google-credentials", "", "Path to Google service account credentials JSON file", withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.googleCredsRef, "google-credentials-ref", "", "Secret reference to Google service account credentials JSON", withPlaceholder("<ref>")),
		stringFlag(&a.googleCredsJSON, "google-credentials-json", "", "Inline Google credentials JSON", withPlaceholder("<json>"), asSecret()),
		stringFlag(&a.googleTokenFile, "google-token-file", "", "Path to Google OAuth token file", withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.googleTokenRef, "google-token-ref", "", "Secret reference to Google OAuth token", withPlaceholder("<ref>")),
		stringFlag(&a.onedriveClientID, "onedrive-client-id", "", "OneDrive OAuth client ID", withPlaceholder("<id>")),
		stringFlag(&a.onedriveTokenFile, "onedrive-token-file", "", "Path to OneDrive OAuth token file", withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.onedriveTokenRef, "onedrive-token-ref", "", "Secret reference to OneDrive OAuth token", withPlaceholder("<ref>")),
	}}
}

func runProfileNew(r *runner, ctx context.Context, a *profileNewArgs) int {
	name, err := onboarding.Resolve(ctx, prompterFor(r), a.name, onboarding.Field{
		Label:    "Profile name",
		Missing:  "-name is required",
		Validate: func(v string) error { return validateRefName("profile", v) },
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

	// When editing an existing profile, prefill unset fields with current values.
	if existing, ok := cfg.Profiles[a.name]; ok {
		prefillProfileArgs(a, existing)
	}

	source, err := onboarding.Resolve(ctx, prompterFor(r), a.source, onboarding.Field{
		Label:   "Source URI",
		Noun:    "source URI",
		Missing: "-source is required",
		Validate: func(v string) error {
			if _, err := config.ParseSourceURI(v); err != nil {
				return fmt.Errorf("invalid source: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return r.fail("%v", err)
	}
	a.source = source
	if a.store != "" && a.storeRef == "" {
		ref, err := onboarding.Resolve(ctx, prompterFor(r), "", onboarding.Field{
			Label:   "Store reference name",
			Default: "default-store",
			Missing: "-store requires -store-ref",
		})
		if err != nil {
			return r.fail("%v", err)
		}
		a.storeRef = ref
	}

	createdStore := false

	if a.store != "" {
		if _, err := config.ParseStoreURI(a.store); err != nil {
			return r.fail("Invalid store URI: %v", err)
		}
		cfg.Stores[a.storeRef] = profile.Store{URI: a.store}
		createdStore = true
	} else if a.storeRef != "" {
		if _, ok := cfg.Stores[a.storeRef]; !ok {
			return r.fail("Unknown store reference %q (use -store to create it)", a.storeRef)
		}
	} else {
		// No store provided — prompt or fail.
		if r.canPrompt() {
			ref, created, selErr := promptStoreSelection(r, ctx, cfg)
			if selErr != nil {
				return r.fail("Failed to %v", selErr)
			}
			a.storeRef = ref
			createdStore = created
		}
		if a.storeRef == "" {
			return r.fail("-store-ref is required (or provide -store to create a new one)")
		}
	}

	if createdStore && r.canPrompt() {
		s := cfg.Stores[a.storeRef]
		if !storeHasExplicitEncryption(s) {
			promptEncryptionConfig(r, ctx, cfg, a.storeRef, a.profilesFile, a.configDir)
		}
		if err := checkOrInitStoreWithRecovery(r, ctx, cfg, a.storeRef, a.profilesFile, checkOrInitOptions{
			configDir:            a.configDir,
			allowMissingSecrets:  true,
			warnOnMissingSecrets: true,
			offerInit:            true,
		}, true); err != nil {
			_, _ = fmt.Fprintf(r.errOut, "%v\n", err)
		}
	}

	requiredProvider := profileProviderFromSource(a.source)

	if a.authRef != "" {
		if requiredProvider == "" {
			return r.fail("-auth-ref requires a cloud source (gdrive/gdrive-changes/onedrive/onedrive-changes)")
		}
		auth, exists := cfg.Auth[a.authRef]
		if !exists {
			return r.fail("Unknown auth reference %q (create it with 'cloudstic auth new')", a.authRef)
		}
		if auth.Provider != "" && auth.Provider != requiredProvider {
			return r.fail("Auth reference %q has provider %q, but source requires %q", a.authRef, auth.Provider, requiredProvider)
		}
		if auth.Provider == "google" {
			if a.googleCreds != "" {
				auth.GoogleCreds = a.googleCreds
			}
			if a.googleTokenFile != "" {
				auth.GoogleTokenFile = a.googleTokenFile
			}
		}
		if auth.Provider == "onedrive" {
			if a.onedriveClientID != "" {
				auth.OneDriveClientID = a.onedriveClientID
			}
			if a.onedriveTokenFile != "" {
				auth.OneDriveTokenFile = a.onedriveTokenFile
			}
		}
		cfg.Auth[a.authRef] = auth
	} else if requiredProvider != "" {
		// Cloud source without -auth-ref — prompt or fail.
		if r.canPrompt() {
			ref, selErr := promptAuthSelection(r, ctx, cfg, requiredProvider, a.name)
			if selErr != nil {
				return r.fail("Failed to %v", selErr)
			}
			a.authRef = ref
		}
		if a.authRef == "" {
			return r.fail("-auth-ref is required for cloud sources (or use 'cloudstic auth new' to create one)")
		}
	}

	p := profile.Profile{
		Source:            a.source,
		Store:             a.storeRef,
		AuthRef:           a.authRef,
		Tags:              []string(a.tags),
		Excludes:          []string(a.excludes),
		ExcludeFile:       a.excludeFile,
		IgnoreEmpty:       a.ignoreEmpty,
		SkipNativeFiles:   a.skipNativeFiles,
		VolumeUUID:        a.volumeUUID,
		GoogleCreds:       a.googleCreds,
		GoogleCredsRef:    a.googleCredsRef,
		GoogleCredsJSON:   a.googleCredsJSON,
		GoogleTokenFile:   a.googleTokenFile,
		GoogleTokenRef:    a.googleTokenRef,
		OneDriveClientID:  a.onedriveClientID,
		OneDriveTokenFile: a.onedriveTokenFile,
		OneDriveTokenRef:  a.onedriveTokenRef,
	}
	cfg.Profiles[a.name] = p

	if err := profile.Save(a.profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}

	_, _ = fmt.Fprintf(r.out, "Profile %q saved in %s\n", a.name, a.profilesFile)
	return 0
}

// profileCommand declares the `profile` command group.
func profileCommand() command {
	return group("profile", "Manage backup profiles",
		leaf("new", "Create or update a backup profile in profiles.yaml", nil, declareProfileNewArgs, runProfileNew),
		leaf("list", "List stores, auth entries, and backup profiles", nil, declareProfileListArgs, runProfileList),
		leaf("show", "Show one profile and resolved store/auth references", nil, declareProfileShowArgs, runProfileShow),
	)
}
