package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/paths"
	"github.com/cloudstic/cli/internal/secretref"
	"github.com/cloudstic/cli/pkg/source"
)

type backupArgs struct {
	g                 *globalFlags
	sourceURI         string
	profile           string
	allProfiles       bool
	authRef           string
	profilesFile      string
	dryRun            bool
	ignoreEmpty       bool
	excludeFile       string
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
	skipMode          bool
	skipFlags         bool
	skipXattrs        bool
	xattrNamespaces   string
	tags              stringArrayFlags
	excludes          stringArrayFlags
	flagsSet          map[string]bool
	sources           map[string]valueSource
}

func backupCommandSpec() *commandSpec {
	return leaf("backup", "Create a new backup snapshot from a source", "", runBackup,
		sourceFlag(), boolFlag("all-profiles", "Run all enabled profiles"),
		valueFlag("auth-ref", "name", "Named cloud auth entry", completionAuth), boolFlag("dry-run", "Scan without writing"),
		boolFlag("ignore-empty-snapshot", "Skip an unchanged snapshot"), boolFlag("skip-native-files", "Exclude cloud-native files"),
		valueFlag("exclude-file", "path", "Exclude-pattern file", completionFile), volumeUUIDFlag(),
		googleCredentialsFlag(), valueFlag("google-credentials-ref", "ref", "Google credentials secret reference", completionNone),
		googleCredentialsJSONFlag(), googleTokenFileFlag(),
		valueFlag("google-token-ref", "ref", "Google OAuth token reference", completionNone), onedriveClientIDFlag(),
		onedriveTokenFileFlag(), valueFlag("onedrive-token-ref", "ref", "OneDrive token reference", completionNone),
		boolFlag("skip-mode", "Skip POSIX metadata"), boolFlag("skip-flags", "Skip file flags"), boolFlag("skip-xattrs", "Skip extended attributes"),
		valueFlag("xattr-namespaces", "prefixes", "Extended-attribute namespaces", completionNone), valueFlag("tag", "tag", "Snapshot tag", completionNone),
		valueFlag("exclude", "pattern", "Exclude pattern", completionNone)).withGlobalFlags().withNotes(
		"Source URIs: local:<path>, sftp://host/path, gdrive, gdrive-changes, onedrive, or onedrive-changes.",
		"Repeat -tag and -exclude to provide multiple values.")
}

func (a *backupArgs) valueSource(name string) valueSource {
	if source, ok := a.sources[name]; ok {
		return source
	}
	return valueSourceDefault
}

func parseBackupArgs(args []string) (*backupArgs, error) {
	command := backupCommandSpec()
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	a := &backupArgs{}
	a.g = addGlobalFlags(fs)
	sourceURI := fs.String("source", "gdrive", "Source URI: local:<path>, sftp://[user@]host[:port]/<path>, gdrive[://<Drive Name>][/<path>], gdrive-changes[://<Drive Name>][/<path>], onedrive[://<Drive Name>][/<path>], onedrive-changes[://<Drive Name>][/<path>]")
	allProfiles := fs.Bool("all-profiles", false, "Run backup for all enabled profiles from profiles.yaml")
	authRef := fs.String("auth-ref", "", "Use named auth entry from profiles.yaml for cloud source credentials")
	dryRun := fs.Bool("dry-run", false, "Scan source and report changes without writing to the store")
	ignoreEmpty := fs.Bool("ignore-empty-snapshot", false, "Skip creating a new snapshot when nothing changed")
	skipNativeFiles := fs.Bool("skip-native-files", false, "Exclude Google-native files (Docs, Sheets, Slides, etc.) from the backup")
	excludeFile := fs.String("exclude-file", "", "Path to file with exclude patterns (one per line, gitignore syntax)")
	volumeUUID := fs.String("volume-uuid", "", "Override volume UUID for local source (enables cross-machine incremental backup)")
	googleCreds := fs.String("google-credentials", "", "Path to Google service account credentials JSON file")
	googleCredsRef := fs.String("google-credentials-ref", "", "Secret reference to Google service account credentials JSON")
	googleCredsJSON := fs.String("google-credentials-json", "", "Inline Google credentials JSON (OAuth client or service account)")
	googleTokenFile := fs.String("google-token-file", "", "Path to Google OAuth token file")
	googleTokenRef := fs.String("google-token-ref", "", "Secret reference to Google OAuth token")
	onedriveClientID := fs.String("onedrive-client-id", "", "OneDrive OAuth client ID")
	onedriveTokenFile := fs.String("onedrive-token-file", "", "Path to OneDrive OAuth token file")
	onedriveTokenRef := fs.String("onedrive-token-ref", "", "Secret reference to OneDrive OAuth token")
	skipMode := fs.Bool("skip-mode", false, "Skip POSIX mode, uid, gid, btime, and flags collection")
	skipFlags := fs.Bool("skip-flags", false, "Skip file flags collection")
	skipXattrs := fs.Bool("skip-xattrs", false, "Skip extended attribute collection")
	xattrNamespaces := fs.String("xattr-namespaces", "", "Restrict xattr collection to these prefixes (comma-separated, e.g. \"user.,com.apple.\")")
	fs.Var(&a.tags, "tag", "Tag to apply to the snapshot (can be specified multiple times)")
	fs.Var(&a.excludes, "exclude", "Exclude pattern (gitignore syntax, repeatable)")
	if err := parseFlags(fs, args, command); err != nil {
		return nil, err
	}
	a.sourceURI = *sourceURI
	a.profile = a.g.profile
	a.allProfiles = *allProfiles
	a.authRef = *authRef
	a.profilesFile = a.g.profilesFile
	a.dryRun = *dryRun
	a.ignoreEmpty = *ignoreEmpty
	a.skipNativeFiles = *skipNativeFiles
	a.excludeFile = *excludeFile
	a.volumeUUID = *volumeUUID
	a.googleCreds = *googleCreds
	a.googleCredsRef = *googleCredsRef
	a.googleCredsJSON = *googleCredsJSON
	a.googleTokenFile = *googleTokenFile
	a.googleTokenRef = *googleTokenRef
	a.onedriveClientID = *onedriveClientID
	a.onedriveTokenFile = *onedriveTokenFile
	a.onedriveTokenRef = *onedriveTokenRef
	a.skipMode = *skipMode
	a.skipFlags = *skipFlags
	a.skipXattrs = *skipXattrs
	a.xattrNamespaces = *xattrNamespaces
	a.sources = commandValueSources(fs, command)
	a.flagsSet = suppliedFlags(a.sources)
	return a, nil
}

func runBackup(r *runner, ctx context.Context) int {
	a, err := parseBackupArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}

	if a.profile != "" && a.allProfiles {
		return r.fail("-profile and -all-profiles are mutually exclusive")
	}

	if a.profile != "" || a.allProfiles {
		return runBackupWithProfiles(r, ctx, a)
	}

	if a.authRef != "" {
		cfg, err := cloudstic.LoadProfilesFile(a.profilesFile)
		if err != nil {
			return r.fail("Failed to load profiles: %v", err)
		}
		authCfg, ok := cfg.Auth[a.authRef]
		if !ok {
			return r.fail("Unknown auth reference %q", a.authRef)
		}
		if err := applyProfileAuthToBackupArgs(a, authCfg); err != nil {
			return r.fail("Auth reference %q: %v", a.authRef, err)
		}
	}

	return runSingleBackup(r, ctx, a)
}

func runSingleBackup(r *runner, ctx context.Context, a *backupArgs) int {
	if err := ensureDefaultAuthRefForCloudBackup(a); err != nil {
		return r.fail("Failed to prepare auth settings: %v", err)
	}

	excludePatterns, err := parseExcludePatterns(a)
	if err != nil {
		return r.fail("Failed to read exclude file: %v", err)
	}

	src, err := initSource(ctx, initSourceOptions{
		sourceURI:         a.sourceURI,
		skipNativeFiles:   a.skipNativeFiles,
		volumeUUID:        a.volumeUUID,
		googleCreds:       a.googleCreds,
		googleCredsRef:    a.googleCredsRef,
		googleCredsJSON:   a.googleCredsJSON,
		googleTokenFile:   a.googleTokenFile,
		googleTokenRef:    a.googleTokenRef,
		onedriveClientID:  a.onedriveClientID,
		onedriveTokenFile: a.onedriveTokenFile,
		onedriveTokenRef:  a.onedriveTokenRef,
		skipMode:          a.skipMode,
		skipFlags:         a.skipFlags,
		skipXattrs:        a.skipXattrs,
		xattrNamespaces:   a.xattrNamespaces,
		globalFlags:       a.g,
		excludePatterns:   excludePatterns,
	})
	if err != nil {
		return r.fail("Failed to init source: %v", err)
	}

	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	backupOpts := buildBackupOpts(a, excludePatterns)

	result, err := r.client.Backup(ctx, src, backupOpts...)
	if err != nil {
		return r.fail("Backup failed: %v", err)
	}
	if a.g.jsonEnabled() {
		return r.writeJSON(result)
	}
	printBackupSummary(r.out, result)
	return 0
}

func ensureDefaultAuthRefForCloudBackup(a *backupArgs) error {
	uri, err := parseSourceURI(a.sourceURI)
	if err != nil {
		return err
	}

	var (
		provider             string
		defaultAuthRef       string
		defaultTokenFilename string
		getToken             func() string
		setToken             func(string)
	)

	switch uri.scheme {
	case "gdrive", "gdrive-changes":
		provider = "google"
		defaultAuthRef = "google-default"
		defaultTokenFilename = "google_token.json"
		getToken = func() string { return a.googleTokenFile }
		setToken = func(v string) { a.googleTokenFile = v }
	case "onedrive", "onedrive-changes":
		provider = "onedrive"
		defaultAuthRef = "onedrive-default"
		defaultTokenFilename = "onedrive_token.json"
		getToken = func() string { return a.onedriveTokenFile }
		setToken = func(v string) { a.onedriveTokenFile = v }
	default:
		return nil
	}

	if a.authRef == "" {
		authRef := defaultAuthRef
		a.authRef = authRef
		if a.flagsSet == nil {
			a.flagsSet = map[string]bool{}
		}

		cfg, loadErr := cloudstic.LoadProfilesFile(a.profilesFile)
		if loadErr != nil {
			if errors.Is(loadErr, os.ErrNotExist) {
				cfg = &cloudstic.ProfilesConfig{Version: 1}
			} else {
				return fmt.Errorf("load profiles for default auth: %w", loadErr)
			}
		}
		if cfg.Auth == nil {
			cfg.Auth = map[string]cloudstic.ProfileAuth{}
		}

		tokenPath := getToken()
		if tokenPath == "" {
			resolved, resolveErr := resolveTokenPath("", defaultTokenFilename)
			if resolveErr != nil {
				return resolveErr
			}
			tokenPath = resolved
			setToken(tokenPath)
		}

		auth := cfg.Auth[authRef]
		if auth.Provider != "" && auth.Provider != provider {
			return fmt.Errorf("default auth %q has provider %q, expected %q", authRef, auth.Provider, provider)
		}
		auth.Provider = provider
		if provider == "google" {
			if a.googleCreds != "" {
				auth.GoogleCreds = a.googleCreds
			}
			if a.googleCredsJSON != "" {
				auth.GoogleCredsJSON = a.googleCredsJSON
			}
			auth.GoogleTokenFile = tokenPath
		}
		if provider == "onedrive" {
			if a.onedriveClientID != "" {
				auth.OneDriveClientID = a.onedriveClientID
			}
			auth.OneDriveTokenFile = tokenPath
		}
		cfg.Auth[authRef] = auth

		if saveErr := cloudstic.SaveProfilesFile(a.profilesFile, cfg); saveErr != nil {
			return fmt.Errorf("save profiles with default auth: %w", saveErr)
		}

		if err := applyProfileAuthToBackupArgs(a, auth); err != nil {
			return err
		}
	}

	return nil
}

func runBackupWithProfiles(r *runner, ctx context.Context, base *backupArgs) int {
	cfg, err := cloudstic.LoadProfilesFile(base.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}

	var names []string
	if base.profile != "" {
		if _, ok := cfg.Profiles[base.profile]; !ok {
			return r.fail("Unknown profile %q", base.profile)
		}
		names = []string{base.profile}
	} else {
		for name, p := range cfg.Profiles {
			if p.IsEnabled() {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return r.fail("No enabled profiles found")
		}
		slices.Sort(names)
	}

	failures := 0
	for _, name := range names {
		p := cfg.Profiles[name]
		effective, err := mergeProfileBackupArgs(base, name, p, cfg)
		if err != nil {
			_, _ = fmt.Fprintf(r.errOut, "[%s] profile merge failed: %v\n", name, err)
			failures++
			continue
		}
		_, _ = fmt.Fprintf(r.out, "\n== Running profile %s ==\n", name)
		r.client = nil // each profile may target a different store
		if code := runSingleBackup(r, ctx, effective); code != 0 {
			failures++
			if !base.allProfiles {
				return code
			}
		}
	}
	if failures > 0 {
		return r.fail("%d profile backup(s) failed", failures)
	}
	return 0
}

func mergeProfileBackupArgs(base *backupArgs, profileName string, p cloudstic.BackupProfile, cfg *cloudstic.ProfilesConfig) (*backupArgs, error) {
	g := cloneGlobalFlags(base.g)
	a := *base
	a.g = g
	a.sources = make(map[string]valueSource, len(base.sources))
	for name, source := range base.sources {
		a.sources[name] = source
	}

	if !a.flagsSet["source"] {
		a.sourceURI = p.Source
		a.sources["source"] = valueSourceProfile
	}
	if a.sourceURI == "" {
		return nil, fmt.Errorf("profile %q has empty source", profileName)
	}

	if !a.flagsSet["skip-native-files"] {
		a.skipNativeFiles = p.SkipNativeFiles
		a.sources["skip-native-files"] = valueSourceProfile
	}
	if !a.flagsSet["volume-uuid"] && p.VolumeUUID != "" {
		a.volumeUUID = p.VolumeUUID
		a.sources["volume-uuid"] = valueSourceProfile
	}
	if !a.flagsSet["google-credentials"] && p.GoogleCreds != "" {
		a.googleCreds = p.GoogleCreds
		a.sources["google-credentials"] = valueSourceProfile
	}
	if !a.flagsSet["google-credentials-ref"] && p.GoogleCredsRef != "" {
		a.googleCredsRef = p.GoogleCredsRef
		a.sources["google-credentials-ref"] = valueSourceProfile
	}
	if !a.flagsSet["google-credentials-json"] && p.GoogleCredsJSON != "" {
		a.googleCredsJSON = p.GoogleCredsJSON
		a.sources["google-credentials-json"] = valueSourceProfile
	}
	if !a.flagsSet["google-token-file"] && p.GoogleTokenFile != "" {
		a.googleTokenFile = p.GoogleTokenFile
		a.sources["google-token-file"] = valueSourceProfile
	}
	if !a.flagsSet["google-token-ref"] && p.GoogleTokenRef != "" {
		a.googleTokenRef = p.GoogleTokenRef
		a.sources["google-token-ref"] = valueSourceProfile
	}
	if !a.flagsSet["onedrive-client-id"] && p.OneDriveClientID != "" {
		a.onedriveClientID = p.OneDriveClientID
		a.sources["onedrive-client-id"] = valueSourceProfile
	}
	if !a.flagsSet["onedrive-token-file"] && p.OneDriveTokenFile != "" {
		a.onedriveTokenFile = p.OneDriveTokenFile
		a.sources["onedrive-token-file"] = valueSourceProfile
	}
	if !a.flagsSet["onedrive-token-ref"] && p.OneDriveTokenRef != "" {
		a.onedriveTokenRef = p.OneDriveTokenRef
		a.sources["onedrive-token-ref"] = valueSourceProfile
	}

	if len(a.tags) == 0 && len(p.Tags) > 0 {
		a.tags = append(stringArrayFlags{}, p.Tags...)
		a.sources["tag"] = valueSourceProfile
	}
	if len(a.excludes) == 0 && len(p.Excludes) > 0 {
		a.excludes = append(stringArrayFlags{}, p.Excludes...)
		a.sources["exclude"] = valueSourceProfile
	}
	if !a.flagsSet["exclude-file"] && p.ExcludeFile != "" {
		a.excludeFile = p.ExcludeFile
		a.sources["exclude-file"] = valueSourceProfile
	}
	if !a.flagsSet["ignore-empty-snapshot"] {
		a.ignoreEmpty = p.IgnoreEmpty
		a.sources["ignore-empty-snapshot"] = valueSourceProfile
	}

	if p.Store != "" {
		storeCfg, ok := cfg.Stores[p.Store]
		if !ok {
			return nil, fmt.Errorf("profile %q references unknown store %q", profileName, p.Store)
		}
		if err := applyProfileStoreToGlobalFlags(g, storeCfg, a.flagsSet); err != nil {
			return nil, fmt.Errorf("profile %q store %q: %w", profileName, p.Store, err)
		}
	}

	if p.AuthRef != "" {
		effectiveAuthRef := p.AuthRef
		if a.flagsSet["auth-ref"] {
			effectiveAuthRef = a.authRef
		}
		authCfg, ok := cfg.Auth[effectiveAuthRef]
		if !ok {
			return nil, fmt.Errorf("profile %q references unknown auth %q", profileName, effectiveAuthRef)
		}
		if err := applyProfileAuthToBackupArgs(&a, authCfg); err != nil {
			return nil, fmt.Errorf("profile %q auth %q: %w", profileName, effectiveAuthRef, err)
		}
	} else if a.flagsSet["auth-ref"] {
		authCfg, ok := cfg.Auth[a.authRef]
		if !ok {
			return nil, fmt.Errorf("profile %q requested unknown auth %q", profileName, a.authRef)
		}
		if err := applyProfileAuthToBackupArgs(&a, authCfg); err != nil {
			return nil, fmt.Errorf("profile %q auth %q: %w", profileName, a.authRef, err)
		}
	}

	return &a, nil
}

func applyProfileAuthToBackupArgs(a *backupArgs, auth cloudstic.ProfileAuth) error {
	if a.sources == nil {
		a.sources = map[string]valueSource{}
	}
	uri, err := parseSourceURI(a.sourceURI)
	if err != nil {
		return fmt.Errorf("parse source URI: %w", err)
	}

	requiredProvider := ""
	switch uri.scheme {
	case "gdrive", "gdrive-changes":
		requiredProvider = "google"
	case "onedrive", "onedrive-changes":
		requiredProvider = "onedrive"
	default:
		return fmt.Errorf("auth refs are only valid for Google Drive and OneDrive sources")
	}

	if auth.Provider != "" && auth.Provider != requiredProvider {
		return fmt.Errorf("provider mismatch: source requires %q but auth entry is %q", requiredProvider, auth.Provider)
	}

	if requiredProvider == "google" {
		if !a.flagsSet["google-credentials"] && auth.GoogleCreds != "" {
			a.googleCreds = auth.GoogleCreds
			a.sources["google-credentials"] = valueSourceProfile
		}
		if !a.flagsSet["google-credentials-ref"] && auth.GoogleCredsRef != "" {
			a.googleCredsRef = auth.GoogleCredsRef
			a.sources["google-credentials-ref"] = valueSourceProfile
		}
		if !a.flagsSet["google-credentials-json"] && auth.GoogleCredsJSON != "" {
			a.googleCredsJSON = auth.GoogleCredsJSON
			a.sources["google-credentials-json"] = valueSourceProfile
		}
		if !a.flagsSet["google-token-file"] && auth.GoogleTokenFile != "" {
			a.googleTokenFile = auth.GoogleTokenFile
			a.sources["google-token-file"] = valueSourceProfile
		}
		if !a.flagsSet["google-token-ref"] && auth.GoogleTokenRef != "" {
			a.googleTokenRef = auth.GoogleTokenRef
			a.sources["google-token-ref"] = valueSourceProfile
		}
	}

	if requiredProvider == "onedrive" {
		if !a.flagsSet["onedrive-client-id"] && auth.OneDriveClientID != "" {
			a.onedriveClientID = auth.OneDriveClientID
			a.sources["onedrive-client-id"] = valueSourceProfile
		}
		if !a.flagsSet["onedrive-token-file"] && auth.OneDriveTokenFile != "" {
			a.onedriveTokenFile = auth.OneDriveTokenFile
			a.sources["onedrive-token-file"] = valueSourceProfile
		}
		if !a.flagsSet["onedrive-token-ref"] && auth.OneDriveTokenRef != "" {
			a.onedriveTokenRef = auth.OneDriveTokenRef
			a.sources["onedrive-token-ref"] = valueSourceProfile
		}
	}

	return nil
}

// cloneGlobalFlags returns an independent value/provenance copy of src. The
// immutable FlagSet and debug writer remain shared across per-profile runs.
func cloneGlobalFlags(src *globalFlags) *globalFlags {
	clone := *src
	if src.sources != nil {
		clone.sources = make(map[string]valueSource, len(src.sources))
		for name, source := range src.sources {
			clone.sources[name] = source
		}
	}
	return &clone
}

func applyProfileStoreToGlobalFlags(g *globalFlags, s cloudstic.ProfileStore, flagsSet map[string]bool) error {
	if !flagsSet["store"] && s.URI != "" {
		g.store = s.URI
		g.setValueSource("store", valueSourceProfile)
	}
	if !flagsSet["s3-endpoint"] && s.S3Endpoint != "" {
		g.s3Endpoint = s.S3Endpoint
		g.setValueSource("s3-endpoint", valueSourceProfile)
	}
	if !flagsSet["s3-region"] && s.S3Region != "" {
		g.s3Region = s.S3Region
		g.setValueSource("s3-region", valueSourceProfile)
	}
	if !flagsSet["s3-profile"] {
		v, err := resolveProfileStoreValue("s3_profile", s.S3Profile, "")
		if err != nil {
			return err
		}
		g.s3Profile = v
		if v != "" {
			g.setValueSource("s3-profile", valueSourceProfile)
		}
	}
	if !flagsSet["s3-access-key"] {
		v, err := resolveProfileStoreValue("s3_access_key", s.S3AccessKey, s.S3AccessKeySecret)
		if err != nil {
			return err
		}
		g.s3AccessKey = v
		if v != "" {
			g.setValueSource("s3-access-key", valueSourceProfile)
		}
	}
	if !flagsSet["s3-secret-key"] {
		v, err := resolveProfileStoreValue("s3_secret_key", s.S3SecretKey, s.S3SecretKeySecret)
		if err != nil {
			return err
		}
		g.s3SecretKey = v
		if v != "" {
			g.setValueSource("s3-secret-key", valueSourceProfile)
		}
	}
	if !flagsSet["b2-key-id"] {
		v, err := resolveProfileStoreValue("b2_key_id", s.B2KeyID, s.B2KeyIDSecret)
		if err != nil {
			return err
		}
		g.b2KeyID = v
		if v != "" {
			g.setValueSource("b2-key-id", valueSourceProfile)
		}
	}
	if !flagsSet["b2-app-key"] {
		v, err := resolveProfileStoreValue("b2_app_key", s.B2AppKey, s.B2AppKeySecret)
		if err != nil {
			return err
		}
		g.b2AppKey = v
		if v != "" {
			g.setValueSource("b2-app-key", valueSourceProfile)
		}
	}
	if !flagsSet["store-sftp-password"] {
		v, err := resolveProfileStoreValue("store_sftp_password", s.StoreSFTPPassword, s.StoreSFTPPasswordSecret)
		if err != nil {
			return err
		}
		g.storeSFTPPassword = v
		if v != "" {
			g.setValueSource("store-sftp-password", valueSourceProfile)
		}
	}
	if !flagsSet["store-sftp-key"] {
		v, err := resolveProfileStoreValue("store_sftp_key", s.StoreSFTPKey, s.StoreSFTPKeySecret)
		if err != nil {
			return err
		}
		g.storeSFTPKey = v
		if v != "" {
			g.setValueSource("store-sftp-key", valueSourceProfile)
		}
	}
	if !flagsSet["password"] {
		v, err := resolveProfileStoreValue("password", "", s.PasswordSecret)
		if err != nil {
			return err
		}
		g.password = v
		if v != "" {
			g.setValueSource("password", valueSourceProfile)
		}
	}
	if !flagsSet["encryption-key"] {
		v, err := resolveProfileStoreValue("encryption_key", "", s.EncryptionKeySecret)
		if err != nil {
			return err
		}
		g.encryptionKey = v
		if v != "" {
			g.setValueSource("encryption-key", valueSourceProfile)
		}
	}
	if !flagsSet["recovery-key"] {
		v, err := resolveProfileStoreValue("recovery_key", "", s.RecoveryKeySecret)
		if err != nil {
			return err
		}
		g.recoveryKey = v
		if v != "" {
			g.setValueSource("recovery-key", valueSourceProfile)
		}
	}
	if !flagsSet["kms-key-arn"] && s.KMSKeyARN != "" {
		g.kmsKeyARN = s.KMSKeyARN
		g.setValueSource("kms-key-arn", valueSourceProfile)
	}
	if !flagsSet["kms-region"] && s.KMSRegion != "" {
		g.kmsRegion = s.KMSRegion
		g.setValueSource("kms-region", valueSourceProfile)
	}
	if !flagsSet["kms-endpoint"] && s.KMSEndpoint != "" {
		g.kmsEndpoint = s.KMSEndpoint
		g.setValueSource("kms-endpoint", valueSourceProfile)
	}
	return nil
}

func parseExcludePatterns(a *backupArgs) ([]string, error) {
	excludePatterns := []string(a.excludes)
	if a.excludeFile != "" {
		filePatterns, err := source.ParseExcludeFile(a.excludeFile)
		if err != nil {
			return nil, err
		}
		excludePatterns = append(excludePatterns, filePatterns...)
	}
	return excludePatterns, nil
}

func buildBackupOpts(a *backupArgs, excludePatterns []string) []cloudstic.BackupOption {
	var opts []cloudstic.BackupOption
	if a.g.verbose {
		opts = append(opts, cloudstic.WithVerbose())
	}
	if a.dryRun {
		opts = append(opts, engine.WithBackupDryRun())
	}
	if a.ignoreEmpty {
		opts = append(opts, cloudstic.WithIgnoreEmptySnapshot())
	}
	if len(a.tags) > 0 {
		opts = append(opts, cloudstic.WithTags(a.tags...))
	}
	if len(excludePatterns) > 0 {
		h := sha256.Sum256([]byte(strings.Join(excludePatterns, "\n")))
		opts = append(opts, cloudstic.WithExcludeHash(hex.EncodeToString(h[:])))
	}
	return opts
}

func printBackupSummary(out io.Writer, res *engine.RunResult) {
	total := res.FilesNew + res.FilesChanged + res.FilesUnmodified +
		res.DirsNew + res.DirsChanged + res.DirsUnmodified
	if res.DryRun {
		_, _ = fmt.Fprintf(out, "\nBackup dry run complete.\n")
	} else if res.EmptySnapshotIgnored {
		_, _ = fmt.Fprintf(out, "\nBackup complete. No new snapshot created; nothing changed. Root: %s\n", res.Root)
	} else {
		_, _ = fmt.Fprintf(out, "\nBackup complete. Snapshot: %s, Root: %s\n", res.SnapshotRef, res.Root)
	}
	_, _ = fmt.Fprintf(out, "Files:  %d new,  %d changed,  %d unmodified,  %d removed\n",
		res.FilesNew, res.FilesChanged, res.FilesUnmodified, res.FilesRemoved)
	_, _ = fmt.Fprintf(out, "Dirs:   %d new,  %d changed,  %d unmodified,  %d removed\n",
		res.DirsNew, res.DirsChanged, res.DirsUnmodified, res.DirsRemoved)
	if !res.DryRun && !res.EmptySnapshotIgnored {
		_, _ = fmt.Fprintf(out, "Added to the repository: %s (%s compressed)\n",
			formatBytes(res.BytesAddedRaw), formatBytes(res.BytesAddedStored))
	}
	_, _ = fmt.Fprintf(out, "Processed %d entries in %s\n",
		total, res.Duration.Round(time.Second))
	if !res.DryRun && !res.EmptySnapshotIgnored {
		_, _ = fmt.Fprintf(out, "Snapshot %s saved\n", res.SnapshotHash)
	}
}

type initSourceOptions struct {
	sourceURI         string
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
	skipMode          bool
	skipFlags         bool
	skipXattrs        bool
	xattrNamespaces   string
	globalFlags       *globalFlags
	excludePatterns   []string
}

func initSource(ctx context.Context, opts initSourceOptions) (source.Source, error) {
	uri, err := parseSourceURI(opts.sourceURI)
	if err != nil {
		return nil, err
	}

	resolver := secretref.NewDefaultResolver()

	switch uri.scheme {
	case "local":
		localOpts := []source.LocalOption{source.WithLocalExcludePatterns(opts.excludePatterns)}
		if opts.volumeUUID != "" {
			localOpts = append(localOpts, source.WithVolumeUUID(opts.volumeUUID))
		}
		if opts.skipMode {
			localOpts = append(localOpts, source.WithSkipMode())
		}
		if opts.skipFlags {
			localOpts = append(localOpts, source.WithSkipFlags())
		}
		if opts.skipXattrs {
			localOpts = append(localOpts, source.WithSkipXattrs())
		}
		if opts.xattrNamespaces != "" {
			prefixes := parseXattrNamespacePrefixes(opts.xattrNamespaces)
			if len(prefixes) > 0 {
				localOpts = append(localOpts, source.WithXattrNamespaces(prefixes))
			}
		}
		return source.NewLocalSource(uri.path, localOpts...), nil
	case "sftp":
		sftpOpts := opts.globalFlags.buildSFTPSourceOpts(uri)
		sftpOpts = append(sftpOpts, source.WithSFTPExcludePatterns(opts.excludePatterns))
		return source.NewSFTPSource(uri.host, sftpOpts...)
	case "gdrive":
		tokenPath, err := resolveTokenPath(opts.googleTokenFile, "google_token.json")
		if err != nil {
			return nil, err
		}
		gdriveOpts := []source.GDriveOption{
			source.WithResolver(resolver),
			source.WithCredsPath(opts.googleCreds),
			source.WithCredsRef(opts.googleCredsRef),
			source.WithCredsJSON([]byte(opts.googleCredsJSON)),
			source.WithTokenPath(tokenPath),
			source.WithTokenRef(opts.googleTokenRef),
			source.WithDriveName(uri.host),
			source.WithRootPath(uri.path),
			source.WithGDriveExcludePatterns(opts.excludePatterns),
		}
		if opts.skipNativeFiles {
			gdriveOpts = append(gdriveOpts, source.WithSkipNativeFiles())
		}
		return source.NewGDriveSource(ctx, gdriveOpts...)
	case "gdrive-changes":
		tokenPath, err := resolveTokenPath(opts.googleTokenFile, "google_token.json")
		if err != nil {
			return nil, err
		}
		gdriveOpts := []source.GDriveOption{
			source.WithResolver(resolver),
			source.WithCredsPath(opts.googleCreds),
			source.WithCredsRef(opts.googleCredsRef),
			source.WithCredsJSON([]byte(opts.googleCredsJSON)),
			source.WithTokenPath(tokenPath),
			source.WithTokenRef(opts.googleTokenRef),
			source.WithDriveName(uri.host),
			source.WithRootPath(uri.path),
			source.WithGDriveExcludePatterns(opts.excludePatterns),
		}
		if opts.skipNativeFiles {
			gdriveOpts = append(gdriveOpts, source.WithSkipNativeFiles())
		}
		return source.NewGDriveChangeSource(ctx, gdriveOpts...)
	case "onedrive":
		tokenPath, err := resolveTokenPath(opts.onedriveTokenFile, "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return source.NewOneDriveSource(ctx,
			source.WithOneDriveResolver(resolver),
			source.WithOneDriveClientID(opts.onedriveClientID),
			source.WithOneDriveTokenPath(tokenPath),
			source.WithOneDriveTokenRef(opts.onedriveTokenRef),
			source.WithOneDriveDriveName(uri.host),
			source.WithOneDriveRootPath(uri.path),
			source.WithOneDriveExcludePatterns(opts.excludePatterns),
		)
	case "onedrive-changes":
		tokenPath, err := resolveTokenPath(opts.onedriveTokenFile, "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return source.NewOneDriveChangeSource(ctx,
			source.WithOneDriveResolver(resolver),
			source.WithOneDriveClientID(opts.onedriveClientID),
			source.WithOneDriveTokenPath(tokenPath),
			source.WithOneDriveTokenRef(opts.onedriveTokenRef),
			source.WithOneDriveDriveName(uri.host),
			source.WithOneDriveRootPath(uri.path),
			source.WithOneDriveExcludePatterns(opts.excludePatterns),
		)
	default:
		return nil, fmt.Errorf("unsupported source: %s", uri.scheme)
	}
}

func parseXattrNamespacePrefixes(raw string) []string {
	parts := strings.Split(raw, ",")
	prefixes := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		prefixes = append(prefixes, p)
	}
	return prefixes
}

// resolveTokenPath returns the token file path to use. If explicit is non-empty
// it is used as-is; otherwise the filename is placed in the cloudstic config dir.
func resolveTokenPath(explicit, defaultFilename string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return paths.TokenPath(defaultFilename)
}
