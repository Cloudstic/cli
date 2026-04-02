package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/paths"
)

const defaultProfilesFilename = "profiles.yaml"

func defaultProfilesPath() (string, error) {
	configDir, err := paths.ConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(configDir, defaultProfilesFilename), nil
}

func (r *runner) runProfile(ctx context.Context) int {
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic profile <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: list, show, new")
		return 1
	}

	switch os.Args[2] {
	case "list":
		return r.runProfileList(ctx)
	case "show":
		return r.runProfileShow(ctx)
	case "new":
		return r.runProfileNew(ctx)
	default:
		return r.fail("Unknown profile subcommand: %s", os.Args[2])
	}
}

type profileShowArgs struct {
	profilesFile string
	name         string
}

func parseProfileShowArgs() (*profileShowArgs, error) {
	fs := flag.NewFlagSet("profile show", flag.ExitOnError)
	a := &profileShowArgs{}
	defaultPath, err := defaultProfilesPath()
	if err != nil {
		defaultPath = defaultProfilesFilename
	}
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultPath), "Path to profiles YAML file")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))
	a.profilesFile = *profilesFile
	if fs.NArg() > 1 {
		return nil, fmt.Errorf("usage: cloudstic profile show [-profiles-file <path>] <name>")
	}
	if fs.NArg() == 1 {
		a.name = fs.Arg(0)
	}
	return a, nil
}

func (r *runner) runProfileShow(ctx context.Context) int {
	a, err := parseProfileShowArgs()
	if err != nil {
		return r.fail("%v", err)
	}
	cfg, err := cloudstic.LoadProfilesFile(a.profilesFile)
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
	r.renderProfileShow(cfg, a.name, p)
	return 0
}

func profileStoreAuthMode(s cloudstic.ProfileStore) string {
	if s.S3AccessKey != "" || s.S3SecretKey != "" || s.S3AccessKeySecret != "" || s.S3SecretKeySecret != "" {
		return "static-keys"
	}
	if s.S3Profile != "" {
		return "aws-shared-profile"
	}
	if s.StoreSFTPPassword != "" || s.StoreSFTPKey != "" || s.StoreSFTPPasswordSecret != "" || s.StoreSFTPKeySecret != "" {
		return "sftp"
	}
	return "default-chain"
}

type profileListArgs struct {
	profilesFile string
}

func parseProfileListArgs() *profileListArgs {
	fs := flag.NewFlagSet("profile list", flag.ExitOnError)
	a := &profileListArgs{}
	defaultPath, err := defaultProfilesPath()
	if err != nil {
		defaultPath = defaultProfilesFilename
	}
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultPath), "Path to profiles YAML file")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))
	a.profilesFile = *profilesFile
	return a
}

func (r *runner) runProfileList(ctx context.Context) int {
	a := parseProfileListArgs()
	cfg, err := cloudstic.LoadProfilesFile(a.profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	r.renderStoreList(cfg)
	_, _ = fmt.Fprintln(r.out)
	r.renderAuthList(cfg)
	_, _ = fmt.Fprintln(r.out)
	r.renderProfileList(cfg)

	return 0
}

type profileNewArgs struct {
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
	flagsSet          map[string]bool
}

func parseProfileNewArgs() *profileNewArgs {
	fs := flag.NewFlagSet("profile new", flag.ExitOnError)
	a := &profileNewArgs{}
	defaultPath, err := defaultProfilesPath()
	if err != nil {
		defaultPath = defaultProfilesFilename
	}
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultPath), "Path to profiles YAML file")
	name := fs.String("name", "", "Profile name")
	source := fs.String("source", "", "Source URI")
	storeRef := fs.String("store-ref", "", "Store reference name from top-level stores map")
	store := fs.String("store", "", "Store URI to create/update under -store-ref")
	authRef := fs.String("auth-ref", "", "Auth reference name from top-level auth map")
	excludeFile := fs.String("exclude-file", "", "Path to file with exclude patterns")
	ignoreEmpty := fs.Bool("ignore-empty-snapshot", false, "Skip creating a new snapshot when nothing changed")
	skipNativeFiles := fs.Bool("skip-native-files", false, "Exclude Google-native files (Docs, Sheets, Slides, etc.)")
	volumeUUID := fs.String("volume-uuid", "", "Override volume UUID for local source")
	googleCreds := fs.String("google-credentials", "", "Path to Google service account credentials JSON file")
	googleCredsRef := fs.String("google-credentials-ref", "", "Secret reference to Google service account credentials JSON")
	googleCredsJSON := fs.String("google-credentials-json", "", "Inline Google credentials JSON")
	googleTokenFile := fs.String("google-token-file", "", "Path to Google OAuth token file")
	googleTokenRef := fs.String("google-token-ref", "", "Secret reference to Google OAuth token")
	onedriveClientID := fs.String("onedrive-client-id", "", "OneDrive OAuth client ID")
	onedriveTokenFile := fs.String("onedrive-token-file", "", "Path to OneDrive OAuth token file")
	onedriveTokenRef := fs.String("onedrive-token-ref", "", "Secret reference to OneDrive OAuth token")
	fs.Var(&a.tags, "tag", "Tag to apply to snapshots (repeatable)")
	fs.Var(&a.excludes, "exclude", "Exclude pattern (repeatable)")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))

	a.flagsSet = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { a.flagsSet[f.Name] = true })

	a.profilesFile = *profilesFile
	a.name = *name
	a.source = *source
	a.storeRef = *storeRef
	a.store = *store
	a.authRef = *authRef
	a.excludeFile = *excludeFile
	a.ignoreEmpty = *ignoreEmpty
	a.skipNativeFiles = *skipNativeFiles
	a.volumeUUID = *volumeUUID
	a.googleCreds = *googleCreds
	a.googleCredsRef = *googleCredsRef
	a.googleCredsJSON = *googleCredsJSON
	a.googleTokenFile = *googleTokenFile
	a.googleTokenRef = *googleTokenRef
	a.onedriveClientID = *onedriveClientID
	a.onedriveTokenFile = *onedriveTokenFile
	a.onedriveTokenRef = *onedriveTokenRef

	return a
}

func (r *runner) runProfileNew(ctx context.Context) int {
	a := parseProfileNewArgs()
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
		cfg.Stores[a.storeRef] = cloudstic.ProfileStore{URI: a.store}
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
		if err := r.checkOrInitStoreWithRecovery(ctx, cfg, a.storeRef, a.profilesFile, checkOrInitOptions{
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

	p := cloudstic.BackupProfile{
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

	if err := cloudstic.SaveProfilesFile(a.profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}

	_, _ = fmt.Fprintf(r.out, "Profile %q saved in %s\n", a.name, a.profilesFile)
	return 0
}

// promptStoreSelection prompts the user to pick an existing store or create a
// new one. It returns the chosen store-ref name, whether a new store was
// created, and exit code 0 on success.
func (r *runner) promptStoreSelection(ctx context.Context, cfg *cloudstic.ProfilesConfig) (string, bool, int) {
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
	cfg.Stores[refName] = cloudstic.ProfileStore{URI: uri}
	return refName, true, 0
}

// promptAuthSelection prompts the user to pick an existing auth entry (filtered
// by provider) or create a new one. It returns the chosen auth-ref name and
// exit code 0 on success. The new entry is added to cfg.Auth in place.
func (r *runner) promptAuthSelection(ctx context.Context, cfg *cloudstic.ProfilesConfig, provider, profileName string) (string, int) {
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

	auth := cloudstic.ProfileAuth{Provider: provider}
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

func prefillProfileArgs(a *profileNewArgs, p cloudstic.BackupProfile) {
	if !a.flagsSet["source"] && p.Source != "" {
		a.source = p.Source
	}
	if !a.flagsSet["store-ref"] && p.Store != "" {
		a.storeRef = p.Store
	}
	if !a.flagsSet["auth-ref"] && p.AuthRef != "" {
		a.authRef = p.AuthRef
	}
	if !a.flagsSet["exclude-file"] && p.ExcludeFile != "" {
		a.excludeFile = p.ExcludeFile
	}
	if !a.flagsSet["skip-native-files"] && p.SkipNativeFiles {
		a.skipNativeFiles = true
	}
	if !a.flagsSet["volume-uuid"] && p.VolumeUUID != "" {
		a.volumeUUID = p.VolumeUUID
	}
	if !a.flagsSet["google-credentials"] && p.GoogleCreds != "" {
		a.googleCreds = p.GoogleCreds
	}
	if !a.flagsSet["google-credentials-ref"] && p.GoogleCredsRef != "" {
		a.googleCredsRef = p.GoogleCredsRef
	}
	if !a.flagsSet["google-credentials-json"] && p.GoogleCredsJSON != "" {
		a.googleCredsJSON = p.GoogleCredsJSON
	}
	if !a.flagsSet["google-token-file"] && p.GoogleTokenFile != "" {
		a.googleTokenFile = p.GoogleTokenFile
	}
	if !a.flagsSet["google-token-ref"] && p.GoogleTokenRef != "" {
		a.googleTokenRef = p.GoogleTokenRef
	}
	if !a.flagsSet["onedrive-client-id"] && p.OneDriveClientID != "" {
		a.onedriveClientID = p.OneDriveClientID
	}
	if !a.flagsSet["onedrive-token-file"] && p.OneDriveTokenFile != "" {
		a.onedriveTokenFile = p.OneDriveTokenFile
	}
	if !a.flagsSet["onedrive-token-ref"] && p.OneDriveTokenRef != "" {
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
	switch uri.scheme {
	case "gdrive", "gdrive-changes":
		return "google"
	case "onedrive", "onedrive-changes":
		return "onedrive"
	default:
		return ""
	}
}
