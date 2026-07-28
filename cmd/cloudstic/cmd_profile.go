package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cloudstic/cli/pkg/profile"

	"github.com/cloudstic/cli/internal/paths"
)

const defaultProfilesFilename = "profiles.yaml"

func defaultProfilesPath() (string, error) {
	return paths.ProfilesPath(defaultProfilesFilename, true)
}

type profileShowArgs struct {
	profilesFile string
	name         string
}

func declareProfileShowArgs(_ *globalFlags) (*profileShowArgs, commandInput) {
	a := &profileShowArgs{}
	return a, commandInput{
		flags:       []flagSpec{profilesFileFlag(&a.profilesFile)},
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

func declareProfileListArgs(_ *globalFlags) (*profileListArgs, commandInput) {
	a := &profileListArgs{}
	return a, commandInput{flags: []flagSpec{profilesFileFlag(&a.profilesFile)}}
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
		profilesFileFlag(&a.profilesFile),
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
	if a.name == "" {
		if r.canPrompt() {
			v, err := r.promptValidatedLine(ctx, "Profile name", "", func(v string) error {
				if v == "" {
					return fmt.Errorf("profile name is required")
				}
				return validateRefName("profile", v)
			})
			if err != nil {
				return r.fail("Failed to read profile name: %v", err)
			}
			a.name = v
		}
		if a.name == "" {
			return r.fail("-name is required")
		}
	}
	if err := validateRefName("profile", a.name); err != nil {
		return r.fail("%v", err)
	}

	cfg, err := loadProfilesOrInit(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	ensureProfilesMaps(cfg)

	// When editing an existing profile, prefill unset fields with current values.
	if existing, ok := cfg.Profiles[a.name]; ok {
		prefillProfileArgs(a, existing)
	}

	if a.source == "" {
		if r.canPrompt() {
			v, err := r.promptValidatedLine(ctx, "Source URI", "", func(v string) error {
				if v == "" {
					return fmt.Errorf("source URI is required")
				}
				_, err := parseSourceURI(v)
				if err != nil {
					return fmt.Errorf("invalid source: %w", err)
				}
				return nil
			})
			if err != nil {
				return r.fail("Failed to read source URI: %v", err)
			}
			a.source = v
		}
		if a.source == "" {
			return r.fail("-source is required")
		}
	}
	if _, err := parseSourceURI(a.source); err != nil {
		return r.fail("Invalid source: %v", err)
	}
	if a.store != "" && a.storeRef == "" {
		if r.canPrompt() {
			v, err := r.promptLine(ctx, "Store reference name", "default-store")
			if err != nil {
				return r.fail("Failed to read store reference: %v", err)
			}
			a.storeRef = v
		}
		if a.storeRef == "" {
			return r.fail("-store requires -store-ref")
		}
	}

	createdStore := false

	if a.store != "" {
		if _, err := parseStoreURI(a.store); err != nil {
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
			ref, created, code := r.promptStoreSelection(ctx, cfg)
			if code != 0 {
				return code
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
			r.promptEncryptionConfig(ctx, cfg, a.storeRef, a.profilesFile)
		}
		if err := checkOrInitStoreWithRecovery(r, ctx, cfg, a.storeRef, a.profilesFile, checkOrInitOptions{
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
			ref, code := r.promptAuthSelection(ctx, cfg, requiredProvider, a.name)
			if code != 0 {
				return code
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

// promptStoreSelection prompts the user to pick an existing store or create a
// new one. It returns the chosen store-ref name, whether a new store was
// created, and exit code 0 on success.
func (r *runner) promptStoreSelection(ctx context.Context, cfg *profile.Config) (string, bool, int) {
	options := []string{"Create new store"}
	for name := range cfg.Stores {
		options = append(options, name)
	}
	sort.Strings(options[1:])

	picked, err := r.promptSelect(ctx, "Select a store", options)
	if err != nil {
		return "", false, r.fail("Failed to select store: %v", err)
	}

	if picked != "Create new store" {
		return picked, false, 0
	}

	// Create a new store inline.
	refName, err := r.promptValidatedLine(ctx, "Store reference name", "default-store", func(v string) error {
		if v == "" {
			return fmt.Errorf("store reference name is required")
		}
		return validateRefName("store", v)
	})
	if err != nil {
		return "", false, r.fail("Failed to read store reference name: %v", err)
	}
	uri, err := r.promptValidatedLine(ctx, "Store URI (e.g. s3:bucket/path, local:/path, sftp://host/path)", "", func(v string) error {
		if v == "" {
			return fmt.Errorf("store URI is required")
		}
		_, err := parseStoreURI(v)
		if err != nil {
			return fmt.Errorf("invalid store URI: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", false, r.fail("Failed to read store URI: %v", err)
	}
	cfg.Stores[refName] = profile.Store{URI: uri}
	return refName, true, 0
}

// promptAuthSelection prompts the user to pick an existing auth entry (filtered
// by provider) or create a new one. It returns the chosen auth-ref name and
// exit code 0 on success. The new entry is added to cfg.Auth in place.
func (r *runner) promptAuthSelection(ctx context.Context, cfg *profile.Config, provider, profileName string) (string, int) {
	options := []string{"Create new auth"}
	for name, auth := range cfg.Auth {
		if auth.Provider == provider {
			options = append(options, name)
		}
	}
	sort.Strings(options[1:])

	picked, err := r.promptSelect(ctx, fmt.Sprintf("Select %s auth entry", provider), options)
	if err != nil {
		return "", r.fail("Failed to select auth entry: %v", err)
	}
	if picked != "Create new auth" {
		return picked, 0
	}

	refName, err := r.promptValidatedLine(ctx, "Auth reference name", provider+"-"+profileName, func(v string) error {
		if v == "" {
			return fmt.Errorf("auth reference name is required")
		}
		return validateRefName("auth", v)
	})
	if err != nil {
		return "", r.fail("Failed to read auth reference name: %v", err)
	}

	defTokenRef := "config-token://" + provider + "/" + refName
	tokenStorage, err := r.promptLine(ctx, "Token storage (file path or secret ref)", defTokenRef)
	if err != nil {
		return "", r.fail("Failed to read token storage: %v", err)
	}
	if tokenStorage == "" {
		tokenStorage = defTokenRef
	}

	auth := profile.Auth{Provider: provider}
	if strings.Contains(tokenStorage, "://") {
		switch provider {
		case "google":
			auth.GoogleTokenRef = tokenStorage
		case "onedrive":
			auth.OneDriveTokenRef = tokenStorage
		}
	} else {
		switch provider {
		case "google":
			auth.GoogleTokenFile = tokenStorage
		case "onedrive":
			auth.OneDriveTokenFile = tokenStorage
		}
	}
	cfg.Auth[refName] = auth
	return refName, 0
}

func prefillProfileArgs(a *profileNewArgs, p profile.Profile) {
	if !a.flagProvided("source") && p.Source != "" {
		a.source = p.Source
	}
	if !a.flagProvided("store-ref") && p.Store != "" {
		a.storeRef = p.Store
	}
	if !a.flagProvided("auth-ref") && p.AuthRef != "" {
		a.authRef = p.AuthRef
	}
	if !a.flagProvided("exclude-file") && p.ExcludeFile != "" {
		a.excludeFile = p.ExcludeFile
	}
	if !a.flagProvided("skip-native-files") && p.SkipNativeFiles {
		a.skipNativeFiles = true
	}
	if !a.flagProvided("volume-uuid") && p.VolumeUUID != "" {
		a.volumeUUID = p.VolumeUUID
	}
	if !a.flagProvided("google-credentials") && p.GoogleCreds != "" {
		a.googleCreds = p.GoogleCreds
	}
	if !a.flagProvided("google-credentials-ref") && p.GoogleCredsRef != "" {
		a.googleCredsRef = p.GoogleCredsRef
	}
	if !a.flagProvided("google-credentials-json") && p.GoogleCredsJSON != "" {
		a.googleCredsJSON = p.GoogleCredsJSON
	}
	if !a.flagProvided("google-token-file") && p.GoogleTokenFile != "" {
		a.googleTokenFile = p.GoogleTokenFile
	}
	if !a.flagProvided("google-token-ref") && p.GoogleTokenRef != "" {
		a.googleTokenRef = p.GoogleTokenRef
	}
	if !a.flagProvided("onedrive-client-id") && p.OneDriveClientID != "" {
		a.onedriveClientID = p.OneDriveClientID
	}
	if !a.flagProvided("onedrive-token-file") && p.OneDriveTokenFile != "" {
		a.onedriveTokenFile = p.OneDriveTokenFile
	}
	if !a.flagProvided("onedrive-token-ref") && p.OneDriveTokenRef != "" {
		a.onedriveTokenRef = p.OneDriveTokenRef
	}
	if len(a.tags) == 0 && len(p.Tags) > 0 {
		a.tags = append(stringArrayFlags{}, p.Tags...)
	}
	if len(a.excludes) == 0 && len(p.Excludes) > 0 {
		a.excludes = append(stringArrayFlags{}, p.Excludes...)
	}
}

func profileProviderFromSource(sourceURI string) string {
	uri, err := parseSourceURI(sourceURI)
	if err != nil {
		return ""
	}
	switch uri.Scheme {
	case "gdrive", "gdrive-changes":
		return "google"
	case "onedrive", "onedrive-changes":
		return "onedrive"
	default:
		return ""
	}
}

// profileCommand declares the `profile` command group.
func profileCommand() command {
	return group("profile", "Manage backup profiles",
		leaf("new", "Create or update a backup profile in profiles.yaml", nil, declareProfileNewArgs, runProfileNew),
		leaf("list", "List stores, auth entries, and backup profiles", nil, declareProfileListArgs, runProfileList),
		leaf("show", "Show one profile and resolved store/auth references", nil, declareProfileShowArgs, runProfileShow),
	)
}
