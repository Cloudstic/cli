package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/paths"
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
	excludeFile       string
	skipNativeFiles   bool
	volumeUUID        string
	googleCreds       string
	googleTokenFile   string
	onedriveClientID  string
	onedriveTokenFile string
	tags              stringArrayFlags
	excludes          stringArrayFlags
	flagsSet          map[string]bool
}

func parseBackupArgs() *backupArgs {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	a := &backupArgs{}
	a.g = addGlobalFlags(fs)
	sourceURI := fs.String("source", envDefault("CLOUDSTIC_SOURCE", "gdrive"), "Source URI: local:<path>, sftp://[user@]host[:port]/<path>, gdrive[://<Drive Name>][/<path>], gdrive-changes[://<Drive Name>][/<path>], onedrive[://<Drive Name>][/<path>], onedrive-changes[://<Drive Name>][/<path>]")
	allProfiles := fs.Bool("all-profiles", false, "Run backup for all enabled profiles from profiles.yaml")
	authRef := fs.String("auth-ref", "", "Use named auth entry from profiles.yaml for cloud source credentials")
	dryRun := fs.Bool("dry-run", false, "Scan source and report changes without writing to the store")
	skipNativeFiles := fs.Bool("skip-native-files", false, "Exclude Google-native files (Docs, Sheets, Slides, etc.) from the backup")
	excludeFile := fs.String("exclude-file", "", "Path to file with exclude patterns (one per line, gitignore syntax)")
	volumeUUID := fs.String("volume-uuid", envDefault("CLOUDSTIC_VOLUME_UUID", ""), "Override volume UUID for local source (enables cross-machine incremental backup)")
	googleCreds := fs.String("google-credentials", envDefault("GOOGLE_APPLICATION_CREDENTIALS", ""), "Path to Google service account credentials JSON file")
	googleTokenFile := fs.String("google-token-file", envDefault("GOOGLE_TOKEN_FILE", ""), "Path to Google OAuth token file")
	onedriveClientID := fs.String("onedrive-client-id", envDefault("ONEDRIVE_CLIENT_ID", ""), "OneDrive OAuth client ID")
	onedriveTokenFile := fs.String("onedrive-token-file", envDefault("ONEDRIVE_TOKEN_FILE", ""), "Path to OneDrive OAuth token file")
	fs.Var(&a.tags, "tag", "Tag to apply to the snapshot (can be specified multiple times)")
	fs.Var(&a.excludes, "exclude", "Exclude pattern (gitignore syntax, repeatable)")
	mustParse(fs)
	a.sourceURI = *sourceURI
	a.profile = *a.g.profile
	a.allProfiles = *allProfiles
	a.authRef = *authRef
	a.profilesFile = *a.g.profilesFile
	a.dryRun = *dryRun
	a.skipNativeFiles = *skipNativeFiles
	a.excludeFile = *excludeFile
	a.volumeUUID = *volumeUUID
	a.googleCreds = *googleCreds
	a.googleTokenFile = *googleTokenFile
	a.onedriveClientID = *onedriveClientID
	a.onedriveTokenFile = *onedriveTokenFile
	a.flagsSet = map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		a.flagsSet[f.Name] = true
	})
	return a
}

func (r *runner) runBackup() int {
	a := parseBackupArgs()

	if a.profile != "" && a.allProfiles {
		return r.fail("-profile and -all-profiles are mutually exclusive")
	}

	if a.profile != "" || a.allProfiles {
		return r.runBackupWithProfiles(a)
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

	return r.runSingleBackup(a)
}

func (r *runner) runSingleBackup(a *backupArgs) int {
	if err := ensureDefaultAuthRefForCloudBackup(a); err != nil {
		return r.fail("Failed to prepare auth settings: %v", err)
	}

	excludePatterns, err := r.parseExcludePatterns(a)
	if err != nil {
		return r.fail("Failed to read exclude file: %v", err)
	}

	ctx := context.Background()

	src, err := initSource(ctx, a.sourceURI, a.skipNativeFiles, a.volumeUUID, a.googleCreds, a.googleTokenFile, a.onedriveClientID, a.onedriveTokenFile, a.g, excludePatterns)
	if err != nil {
		return r.fail("Failed to init source: %v", err)
	}

	if err := r.openClient(a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	backupOpts := buildBackupOpts(a, excludePatterns)

	result, err := r.client.Backup(ctx, src, backupOpts...)
	if err != nil {
		return r.fail("Backup failed: %v", err)
	}
	r.printBackupSummary(result)
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

func (r *runner) runBackupWithProfiles(base *backupArgs) int {
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
		if code := r.runSingleBackup(effective); code != 0 {
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

	if !a.flagsSet["source"] {
		a.sourceURI = p.Source
	}
	if a.sourceURI == "" {
		return nil, fmt.Errorf("profile %q has empty source", profileName)
	}

	if !a.flagsSet["skip-native-files"] {
		a.skipNativeFiles = p.SkipNativeFiles
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
	if !a.flagsSet["exclude-file"] && p.ExcludeFile != "" {
		a.excludeFile = p.ExcludeFile
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
		}
		if !a.flagsSet["google-token-file"] && auth.GoogleTokenFile != "" {
			a.googleTokenFile = auth.GoogleTokenFile
		}
	}

	if requiredProvider == "onedrive" {
		if !a.flagsSet["onedrive-client-id"] && auth.OneDriveClientID != "" {
			a.onedriveClientID = auth.OneDriveClientID
		}
		if !a.flagsSet["onedrive-token-file"] && auth.OneDriveTokenFile != "" {
			a.onedriveTokenFile = auth.OneDriveTokenFile
		}
	}

	return nil
}

func cloneGlobalFlags(src *globalFlags) *globalFlags {
	clone := *src

	store := *src.store
	profile := *src.profile
	profilesFile := *src.profilesFile
	s3Endpoint := *src.s3Endpoint
	s3Region := *src.s3Region
	s3Profile := *src.s3Profile
	s3AccessKey := *src.s3AccessKey
	s3SecretKey := *src.s3SecretKey
	sourceSFTPPassword := *src.sourceSFTPPassword
	sourceSFTPKey := *src.sourceSFTPKey
	storeSFTPPassword := *src.storeSFTPPassword
	storeSFTPKey := *src.storeSFTPKey
	encryptionKey := *src.encryptionKey
	password := *src.password
	recoveryKey := *src.recoveryKey
	kmsKeyARN := *src.kmsKeyARN
	kmsRegion := *src.kmsRegion
	kmsEndpoint := *src.kmsEndpoint
	disablePackfile := *src.disablePackfile
	prompt := *src.prompt
	verbose := *src.verbose
	quiet := *src.quiet
	debug := *src.debug

	clone.store = &store
	clone.profile = &profile
	clone.profilesFile = &profilesFile
	clone.s3Endpoint = &s3Endpoint
	clone.s3Region = &s3Region
	clone.s3Profile = &s3Profile
	clone.s3AccessKey = &s3AccessKey
	clone.s3SecretKey = &s3SecretKey
	clone.sourceSFTPPassword = &sourceSFTPPassword
	clone.sourceSFTPKey = &sourceSFTPKey
	clone.storeSFTPPassword = &storeSFTPPassword
	clone.storeSFTPKey = &storeSFTPKey
	clone.encryptionKey = &encryptionKey
	clone.password = &password
	clone.recoveryKey = &recoveryKey
	clone.kmsKeyARN = &kmsKeyARN
	clone.kmsRegion = &kmsRegion
	clone.kmsEndpoint = &kmsEndpoint
	clone.disablePackfile = &disablePackfile
	clone.prompt = &prompt
	clone.verbose = &verbose
	clone.quiet = &quiet
	clone.debug = &debug

	return &clone
}

func applyProfileStoreToGlobalFlags(g *globalFlags, s cloudstic.ProfileStore, flagsSet map[string]bool) error {
	if !flagsSet["store"] && s.URI != "" {
		*g.store = s.URI
	}
	if !flagsSet["s3-endpoint"] && s.S3Endpoint != "" {
		*g.s3Endpoint = s.S3Endpoint
	}
	if !flagsSet["s3-region"] && s.S3Region != "" {
		*g.s3Region = s.S3Region
	}
	if !flagsSet["s3-profile"] {
		v, err := resolveProfileStoreValue("s3_profile", s.S3Profile, "", s.S3ProfileEnv)
		if err != nil {
			return err
		}
		*g.s3Profile = v
	}
	if !flagsSet["s3-access-key"] {
		v, err := resolveProfileStoreValue("s3_access_key", s.S3AccessKey, s.S3AccessKeySecret, s.S3AccessKeyEnv)
		if err != nil {
			return err
		}
		*g.s3AccessKey = v
	}
	if !flagsSet["s3-secret-key"] {
		v, err := resolveProfileStoreValue("s3_secret_key", s.S3SecretKey, s.S3SecretKeySecret, s.S3SecretKeyEnv)
		if err != nil {
			return err
		}
		*g.s3SecretKey = v
	}
	if !flagsSet["store-sftp-password"] {
		v, err := resolveProfileStoreValue("store_sftp_password", s.StoreSFTPPassword, s.StoreSFTPPasswordSecret, s.StoreSFTPPasswordEnv)
		if err != nil {
			return err
		}
		*g.storeSFTPPassword = v
	}
	if !flagsSet["store-sftp-key"] {
		v, err := resolveProfileStoreValue("store_sftp_key", s.StoreSFTPKey, s.StoreSFTPKeySecret, s.StoreSFTPKeyEnv)
		if err != nil {
			return err
		}
		*g.storeSFTPKey = v
	}
	if !flagsSet["password"] {
		v, err := resolveProfileStoreValue("password", "", s.PasswordSecret, s.PasswordEnv)
		if err != nil {
			return err
		}
		*g.password = v
	}
	if !flagsSet["encryption-key"] {
		v, err := resolveProfileStoreValue("encryption_key", "", s.EncryptionKeySecret, s.EncryptionKeyEnv)
		if err != nil {
			return err
		}
		*g.encryptionKey = v
	}
	if !flagsSet["recovery-key"] {
		v, err := resolveProfileStoreValue("recovery_key", "", s.RecoveryKeySecret, s.RecoveryKeyEnv)
		if err != nil {
			return err
		}
		*g.recoveryKey = v
	}
	if !flagsSet["kms-key-arn"] && s.KMSKeyARN != "" {
		*g.kmsKeyARN = s.KMSKeyARN
	}
	if !flagsSet["kms-region"] && s.KMSRegion != "" {
		*g.kmsRegion = s.KMSRegion
	}
	if !flagsSet["kms-endpoint"] && s.KMSEndpoint != "" {
		*g.kmsEndpoint = s.KMSEndpoint
	}
	return nil
}

func (r *runner) parseExcludePatterns(a *backupArgs) ([]string, error) {
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
	if *a.g.verbose {
		opts = append(opts, cloudstic.WithVerbose())
	}
	if a.dryRun {
		opts = append(opts, engine.WithBackupDryRun())
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

func (r *runner) printBackupSummary(res *engine.RunResult) {
	total := res.FilesNew + res.FilesChanged + res.FilesUnmodified +
		res.DirsNew + res.DirsChanged + res.DirsUnmodified
	if res.DryRun {
		_, _ = fmt.Fprintf(r.out, "\nBackup dry run complete.\n")
	} else {
		_, _ = fmt.Fprintf(r.out, "\nBackup complete. Snapshot: %s, Root: %s\n", res.SnapshotRef, res.Root)
	}
	_, _ = fmt.Fprintf(r.out, "Files:  %d new,  %d changed,  %d unmodified,  %d removed\n",
		res.FilesNew, res.FilesChanged, res.FilesUnmodified, res.FilesRemoved)
	_, _ = fmt.Fprintf(r.out, "Dirs:   %d new,  %d changed,  %d unmodified,  %d removed\n",
		res.DirsNew, res.DirsChanged, res.DirsUnmodified, res.DirsRemoved)
	if !res.DryRun {
		_, _ = fmt.Fprintf(r.out, "Added to the repository: %s (%s compressed)\n",
			formatBytes(res.BytesAddedRaw), formatBytes(res.BytesAddedStored))
	}
	_, _ = fmt.Fprintf(r.out, "Processed %d entries in %s\n",
		total, res.Duration.Round(time.Second))
	if !res.DryRun {
		_, _ = fmt.Fprintf(r.out, "Snapshot %s saved\n", res.SnapshotHash)
	}
}

func initSource(ctx context.Context, sourceURI string, skipNativeFiles bool, volumeUUID, googleCreds, googleTokenFile, onedriveClientID, onedriveTokenFile string, g *globalFlags, excludePatterns []string) (source.Source, error) {
	uri, err := parseSourceURI(sourceURI)
	if err != nil {
		return nil, err
	}

	switch uri.scheme {
	case "local":
		opts := []source.LocalOption{source.WithLocalExcludePatterns(excludePatterns)}
		if volumeUUID != "" {
			opts = append(opts, source.WithVolumeUUID(volumeUUID))
		}
		return source.NewLocalSource(uri.path, opts...), nil
	case "sftp":
		sftpOpts := g.buildSFTPSourceOpts(uri)
		sftpOpts = append(sftpOpts, source.WithSFTPExcludePatterns(excludePatterns))
		return source.NewSFTPSource(uri.host, sftpOpts...)
	case "gdrive":
		tokenPath, err := resolveTokenPath(googleTokenFile, "google_token.json")
		if err != nil {
			return nil, err
		}
		gdriveOpts := []source.GDriveOption{
			source.WithCredsPath(googleCreds),
			source.WithTokenPath(tokenPath),
			source.WithDriveName(uri.host),
			source.WithRootPath(uri.path),
			source.WithGDriveExcludePatterns(excludePatterns),
		}
		if skipNativeFiles {
			gdriveOpts = append(gdriveOpts, source.WithSkipNativeFiles())
		}
		return source.NewGDriveSource(ctx, gdriveOpts...)
	case "gdrive-changes":
		tokenPath, err := resolveTokenPath(googleTokenFile, "google_token.json")
		if err != nil {
			return nil, err
		}
		gdriveOpts := []source.GDriveOption{
			source.WithCredsPath(googleCreds),
			source.WithTokenPath(tokenPath),
			source.WithDriveName(uri.host),
			source.WithRootPath(uri.path),
			source.WithGDriveExcludePatterns(excludePatterns),
		}
		if skipNativeFiles {
			gdriveOpts = append(gdriveOpts, source.WithSkipNativeFiles())
		}
		return source.NewGDriveChangeSource(ctx, gdriveOpts...)
	case "onedrive":
		tokenPath, err := resolveTokenPath(onedriveTokenFile, "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return source.NewOneDriveSource(ctx,
			source.WithOneDriveClientID(onedriveClientID),
			source.WithOneDriveTokenPath(tokenPath),
			source.WithOneDriveDriveName(uri.host),
			source.WithOneDriveRootPath(uri.path),
			source.WithOneDriveExcludePatterns(excludePatterns),
		)
	case "onedrive-changes":
		tokenPath, err := resolveTokenPath(onedriveTokenFile, "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return source.NewOneDriveChangeSource(ctx,
			source.WithOneDriveClientID(onedriveClientID),
			source.WithOneDriveTokenPath(tokenPath),
			source.WithOneDriveDriveName(uri.host),
			source.WithOneDriveRootPath(uri.path),
			source.WithOneDriveExcludePatterns(excludePatterns),
		)
	default:
		return nil, fmt.Errorf("unsupported source: %s", uri.scheme)
	}
}

// resolveTokenPath returns the token file path to use. If explicit is non-empty
// it is used as-is; otherwise the filename is placed in the cloudstic config dir.
func resolveTokenPath(explicit, defaultFilename string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return paths.TokenPath(defaultFilename)
}
