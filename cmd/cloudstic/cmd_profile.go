package main

import (
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

func (r *runner) runProfile() int {
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic profile <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: list, show, new")
		return 1
	}

	switch os.Args[2] {
	case "list":
		return r.runProfileList()
	case "show":
		return r.runProfileShow()
	case "new":
		return r.runProfileNew()
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

func (r *runner) runProfileShow() int {
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
		names := make([]string, 0, len(cfg.Profiles))
		for name := range cfg.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		picked, pickErr := r.promptSelect("Select profile", names)
		if pickErr != nil {
			return r.fail("Failed to select profile: %v", pickErr)
		}
		a.name = picked
	}
	p, ok := cfg.Profiles[a.name]
	if !ok {
		return r.fail("Unknown profile %q", a.name)
	}

	_, _ = fmt.Fprintf(r.out, "profile: %s\n", a.name)
	_, _ = fmt.Fprintf(r.out, "  source: %s\n", p.Source)
	if p.Store != "" {
		_, _ = fmt.Fprintf(r.out, "  store_ref: %s\n", p.Store)
		if s, ok := cfg.Stores[p.Store]; ok {
			_, _ = fmt.Fprintf(r.out, "  store_uri: %s\n", s.URI)
			if uri, parseErr := parseStoreURI(s.URI); parseErr == nil && uri.scheme == "s3" {
				if s.S3Region != "" {
					_, _ = fmt.Fprintf(r.out, "  store_s3_region: %s\n", s.S3Region)
				}
				if s.S3Profile != "" {
					_, _ = fmt.Fprintf(r.out, "  store_s3_profile: %s\n", s.S3Profile)
				}
				if s.S3Endpoint != "" {
					_, _ = fmt.Fprintf(r.out, "  store_s3_endpoint: %s\n", s.S3Endpoint)
				}
			}
			_, _ = fmt.Fprintf(r.out, "  store_auth_mode: %s\n", profileStoreAuthMode(s))
		} else {
			_, _ = fmt.Fprintf(r.out, "  store_uri: <missing ref>\n")
		}
	}
	if p.AuthRef != "" {
		_, _ = fmt.Fprintf(r.out, "  auth_ref: %s\n", p.AuthRef)
		if auth, ok := cfg.Auth[p.AuthRef]; ok {
			_, _ = fmt.Fprintf(r.out, "  auth_provider: %s\n", auth.Provider)
			if auth.GoogleTokenFile != "" {
				_, _ = fmt.Fprintf(r.out, "  google_token_file: %s\n", auth.GoogleTokenFile)
			}
			if auth.OneDriveTokenFile != "" {
				_, _ = fmt.Fprintf(r.out, "  onedrive_token_file: %s\n", auth.OneDriveTokenFile)
			}
		} else {
			_, _ = fmt.Fprintf(r.out, "  auth_provider: <missing ref>\n")
		}
	}
	if len(p.Tags) > 0 {
		_, _ = fmt.Fprintf(r.out, "  tags: %s\n", strings.Join(p.Tags, ", "))
	}
	if len(p.Excludes) > 0 {
		_, _ = fmt.Fprintf(r.out, "  excludes: %s\n", strings.Join(p.Excludes, ", "))
	}
	if p.ExcludeFile != "" {
		_, _ = fmt.Fprintf(r.out, "  exclude_file: %s\n", p.ExcludeFile)
	}
	return 0
}

func profileStoreAuthMode(s cloudstic.ProfileStore) string {
	if s.S3AccessKey != "" || s.S3SecretKey != "" || s.S3AccessKeyEnv != "" || s.S3SecretKeyEnv != "" || s.S3AccessKeySecret != "" || s.S3SecretKeySecret != "" {
		return "static-keys"
	}
	if s.S3Profile != "" || s.S3ProfileEnv != "" {
		return "aws-shared-profile"
	}
	if s.StoreSFTPPassword != "" || s.StoreSFTPKey != "" || s.StoreSFTPPasswordEnv != "" || s.StoreSFTPKeyEnv != "" || s.StoreSFTPPasswordSecret != "" || s.StoreSFTPKeySecret != "" {
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

func (r *runner) runProfileList() int {
	a := parseProfileListArgs()
	cfg, err := cloudstic.LoadProfilesFile(a.profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	storeNames := make([]string, 0, len(cfg.Stores))
	for name := range cfg.Stores {
		storeNames = append(storeNames, name)
	}
	sort.Strings(storeNames)

	_, _ = fmt.Fprintf(r.out, "%d stores\n", len(storeNames))
	for _, name := range storeNames {
		s := cfg.Stores[name]
		_, _ = fmt.Fprintf(r.out, "- %s", name)
		if s.URI != "" {
			_, _ = fmt.Fprintf(r.out, "  uri=%s", s.URI)
		}
		_, _ = fmt.Fprintln(r.out)
	}
	_, _ = fmt.Fprintln(r.out)

	authNames := make([]string, 0, len(cfg.Auth))
	for name := range cfg.Auth {
		authNames = append(authNames, name)
	}
	sort.Strings(authNames)

	_, _ = fmt.Fprintf(r.out, "%d auth entries\n", len(authNames))
	for _, name := range authNames {
		a := cfg.Auth[name]
		_, _ = fmt.Fprintf(r.out, "- %s", name)
		if a.Provider != "" {
			_, _ = fmt.Fprintf(r.out, "  provider=%s", a.Provider)
		}
		if a.Provider == "google" && a.GoogleTokenFile != "" {
			_, _ = fmt.Fprintf(r.out, "  token=%s", a.GoogleTokenFile)
		}
		if a.Provider == "onedrive" && a.OneDriveTokenFile != "" {
			_, _ = fmt.Fprintf(r.out, "  token=%s", a.OneDriveTokenFile)
		}
		_, _ = fmt.Fprintln(r.out)
	}
	_, _ = fmt.Fprintln(r.out)

	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	_, _ = fmt.Fprintf(r.out, "%d profiles\n", len(names))
	for _, name := range names {
		p := cfg.Profiles[name]
		_, _ = fmt.Fprintf(r.out, "- %s", name)
		if p.Source != "" {
			_, _ = fmt.Fprintf(r.out, "  source=%s", p.Source)
		}
		if p.Store != "" {
			_, _ = fmt.Fprintf(r.out, "  store=%s", p.Store)
		}
		if p.AuthRef != "" {
			_, _ = fmt.Fprintf(r.out, "  auth=%s", p.AuthRef)
		}
		_, _ = fmt.Fprintln(r.out)
	}

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
	skipNativeFiles   bool
	volumeUUID        string
	googleCreds       string
	googleTokenFile   string
	onedriveClientID  string
	onedriveTokenFile string
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
	skipNativeFiles := fs.Bool("skip-native-files", false, "Exclude Google-native files (Docs, Sheets, Slides, etc.)")
	volumeUUID := fs.String("volume-uuid", "", "Override volume UUID for local source")
	googleCreds := fs.String("google-credentials", "", "Path to Google service account credentials JSON file")
	googleTokenFile := fs.String("google-token-file", "", "Path to Google OAuth token file")
	onedriveClientID := fs.String("onedrive-client-id", "", "OneDrive OAuth client ID")
	onedriveTokenFile := fs.String("onedrive-token-file", "", "Path to OneDrive OAuth token file")
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
	a.skipNativeFiles = *skipNativeFiles
	a.volumeUUID = *volumeUUID
	a.googleCreds = *googleCreds
	a.googleTokenFile = *googleTokenFile
	a.onedriveClientID = *onedriveClientID
	a.onedriveTokenFile = *onedriveTokenFile

	return a
}

func (r *runner) runProfileNew() int {
	a := parseProfileNewArgs()
	if a.name == "" {
		if r.canPrompt() {
			v, err := r.promptLine("Profile name", "")
			if err != nil {
				return r.fail("Failed to read profile name: %v", err)
			}
			a.name = v
		}
		if a.name == "" {
			return r.fail("-name is required")
		}
	}
	if !validRefName.MatchString(a.name) {
		return r.fail("invalid profile name %q: must start with a letter or digit and contain only letters, digits, dots, hyphens, or underscores", a.name)
	}

	cfg, err := cloudstic.LoadProfilesFile(a.profilesFile)
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
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]cloudstic.BackupProfile{}
	}
	if cfg.Auth == nil {
		cfg.Auth = map[string]cloudstic.ProfileAuth{}
	}

	// When editing an existing profile, prefill unset fields with current values.
	if existing, ok := cfg.Profiles[a.name]; ok {
		prefillProfileArgs(a, existing)
	}

	if a.source == "" {
		if r.canPrompt() {
			v, err := r.promptLine("Source URI", "")
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
			v, err := r.promptLine("Store reference name", "default-store")
			if err != nil {
				return r.fail("Failed to read store reference: %v", err)
			}
			a.storeRef = v
		}
		if a.storeRef == "" {
			return r.fail("-store requires -store-ref")
		}
	}

	if a.store != "" {
		cfg.Stores[a.storeRef] = cloudstic.ProfileStore{URI: a.store}
	} else if a.storeRef != "" {
		if _, ok := cfg.Stores[a.storeRef]; !ok {
			return r.fail("Unknown store reference %q (use -store to create it)", a.storeRef)
		}
	} else {
		// No store provided — prompt or fail.
		if r.canPrompt() {
			ref, code := r.promptStoreSelection(cfg)
			if code != 0 {
				return code
			}
			a.storeRef = ref
		}
		if a.storeRef == "" {
			return r.fail("-store-ref is required (or provide -store to create a new one)")
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
			ref, code := r.promptAuthSelection(cfg, requiredProvider, a.name)
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
		SkipNativeFiles:   a.skipNativeFiles,
		VolumeUUID:        a.volumeUUID,
		GoogleCreds:       a.googleCreds,
		GoogleTokenFile:   a.googleTokenFile,
		OneDriveClientID:  a.onedriveClientID,
		OneDriveTokenFile: a.onedriveTokenFile,
	}
	cfg.Profiles[a.name] = p

	if err := cloudstic.SaveProfilesFile(a.profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}

	_, _ = fmt.Fprintf(r.out, "Profile %q saved in %s\n", a.name, a.profilesFile)
	return 0
}

// promptStoreSelection prompts the user to pick an existing store or create a
// new one. It returns the chosen store-ref name and exit code 0 on success.
func (r *runner) promptStoreSelection(cfg *cloudstic.ProfilesConfig) (string, int) {
	options := []string{"Create new store"}
	for name := range cfg.Stores {
		options = append(options, name)
	}
	sort.Strings(options[1:])

	picked, err := r.promptSelect("Select a store", options)
	if err != nil {
		return "", r.fail("Failed to select store: %v", err)
	}

	if picked != "Create new store" {
		return picked, 0
	}

	// Create a new store inline.
	refName, err := r.promptLine("Store reference name", "default-store")
	if err != nil {
		return "", r.fail("Failed to read store reference name: %v", err)
	}
	if refName == "" {
		return "", r.fail("Store reference name is required")
	}
	uri, err := r.promptLine("Store URI (e.g. s3://bucket/path, local:/path, sftp://host/path)", "")
	if err != nil {
		return "", r.fail("Failed to read store URI: %v", err)
	}
	if uri == "" {
		return "", r.fail("Store URI is required")
	}
	cfg.Stores[refName] = cloudstic.ProfileStore{URI: uri}
	return refName, 0
}

// promptAuthSelection prompts the user to pick an existing auth entry (filtered
// by provider) or create a new one. It returns the chosen auth-ref name and
// exit code 0 on success. The new entry is added to cfg.Auth in place.
func (r *runner) promptAuthSelection(cfg *cloudstic.ProfilesConfig, provider, profileName string) (string, int) {
	options := []string{"Create new auth"}
	for name, auth := range cfg.Auth {
		if auth.Provider == provider {
			options = append(options, name)
		}
	}
	sort.Strings(options[1:])

	picked, err := r.promptSelect(fmt.Sprintf("Select %s auth entry", provider), options)
	if err != nil {
		return "", r.fail("Failed to select auth entry: %v", err)
	}
	if picked != "Create new auth" {
		return picked, 0
	}

	refName, err := r.promptLine("Auth reference name", provider+"-"+profileName)
	if err != nil {
		return "", r.fail("Failed to read auth reference name: %v", err)
	}
	if refName == "" {
		return "", r.fail("Auth reference name is required")
	}

	tokenFile, err := r.promptLine("Token file path", defaultAuthTokenPath(provider, refName))
	if err != nil {
		return "", r.fail("Failed to read token file path: %v", err)
	}
	if tokenFile == "" {
		return "", r.fail("Token file path is required")
	}

	auth := cloudstic.ProfileAuth{Provider: provider}
	switch provider {
	case "google":
		auth.GoogleTokenFile = tokenFile
	case "onedrive":
		auth.OneDriveTokenFile = tokenFile
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
	if !a.flagsSet["google-token-file"] && p.GoogleTokenFile != "" {
		a.googleTokenFile = p.GoogleTokenFile
	}
	if !a.flagsSet["onedrive-client-id"] && p.OneDriveClientID != "" {
		a.onedriveClientID = p.OneDriveClientID
	}
	if !a.flagsSet["onedrive-token-file"] && p.OneDriveTokenFile != "" {
		a.onedriveTokenFile = p.OneDriveTokenFile
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
