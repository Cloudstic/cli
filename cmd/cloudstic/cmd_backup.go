package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/cloudstic/cli/pkg/profile"

	"golang.org/x/crypto/ssh"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/paths"
	secretrefbackends "github.com/cloudstic/cli/pkg/secretref/backends"
	"github.com/cloudstic/cli/pkg/source"
	"github.com/cloudstic/cli/pkg/source/gdrive"
	"github.com/cloudstic/cli/pkg/source/local"
	"github.com/cloudstic/cli/pkg/source/onedrive"

	// Aliased to keep "sftp" free: this file also reaches SSH host-key types,
	// and the store-side SFTP wiring lives alongside it.
	sftpsource "github.com/cloudstic/cli/pkg/source/sftp"
)

type backupArgs struct {
	*globalFlags
	sourceURI         string
	allProfiles       bool
	authRef           string
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
}

func declareBackupArgs(g *globalFlags) (*backupArgs, commandInput) {
	a := &backupArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		stringFlag(&a.sourceURI, "source", "gdrive",
			"Source URI: local:<path>, sftp://[user@]host[:port]/<path>, gdrive[://<Drive Name>][/<path>], gdrive-changes[://<Drive Name>][/<path>], onedrive[://<Drive Name>][/<path>], onedrive-changes[://<Drive Name>][/<path>]",
			withEnv("CLOUDSTIC_SOURCE"), withPlaceholder("<uri>"), withCompleter("_cloudstic_source_prefixes"),
			withShortUsage("Source URI")),
		boolFlag(&a.allProfiles, "all-profiles", false, "Run backup for all enabled profiles from profiles.yaml",
			withShortUsage("Run all enabled backup profiles")),
		stringFlag(&a.authRef, "auth-ref", "", "Use named auth entry from profiles.yaml for cloud source credentials",
			withPlaceholder("<name>"), withCompleter("_cloudstic_auth_names"),
			withShortUsage("Use named auth entry from profiles.yaml")),
		boolFlag(&a.dryRun, "dry-run", false, "Scan source and report changes without writing to the store",
			withShortUsage("Scan without writing")),
		boolFlag(&a.ignoreEmpty, "ignore-empty-snapshot", false, "Skip creating a new snapshot when nothing changed"),
		boolFlag(&a.skipNativeFiles, "skip-native-files", false, "Exclude Google-native files (Docs, Sheets, Slides, etc.) from the backup"),
		stringFlag(&a.excludeFile, "exclude-file", "", "Path to file with exclude patterns (one per line, gitignore syntax)",
			withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.volumeUUID, "volume-uuid", "", "Override volume UUID for local source (enables cross-machine incremental backup)",
			withEnv("CLOUDSTIC_VOLUME_UUID"), withPlaceholder("<uuid>")),
		stringFlag(&a.googleCreds, "google-credentials", "", "Path to Google service account credentials JSON file",
			withEnv("GOOGLE_APPLICATION_CREDENTIALS"), withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.googleCredsRef, "google-credentials-ref", "", "Secret reference to Google service account credentials JSON",
			withPlaceholder("<ref>")),
		stringFlag(&a.googleCredsJSON, "google-credentials-json", "", "Inline Google credentials JSON (OAuth client or service account)",
			withEnv("GOOGLE_CREDENTIALS_JSON"), withPlaceholder("<json>"), asSecret()),
		stringFlag(&a.googleTokenFile, "google-token-file", "", "Path to Google OAuth token file",
			withEnv("GOOGLE_TOKEN_FILE"), withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.googleTokenRef, "google-token-ref", "", "Secret reference to Google OAuth token",
			withPlaceholder("<ref>")),
		stringFlag(&a.onedriveClientID, "onedrive-client-id", "", "OneDrive OAuth client ID",
			withEnv("ONEDRIVE_CLIENT_ID"), withPlaceholder("<id>")),
		stringFlag(&a.onedriveTokenFile, "onedrive-token-file", "", "Path to OneDrive OAuth token file",
			withEnv("ONEDRIVE_TOKEN_FILE"), withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.onedriveTokenRef, "onedrive-token-ref", "", "Secret reference to OneDrive OAuth token",
			withPlaceholder("<ref>")),
		boolFlag(&a.skipMode, "skip-mode", false, "Skip POSIX mode, uid, gid, btime, and flags collection"),
		boolFlag(&a.skipFlags, "skip-flags", false, "Skip file flags collection"),
		boolFlag(&a.skipXattrs, "skip-xattrs", false, "Skip extended attribute collection"),
		stringFlag(&a.xattrNamespaces, "xattr-namespaces", "", "Restrict xattr collection to these prefixes (comma-separated, e.g. \"user.,com.apple.\")",
			withPlaceholder("<prefixes>")),
		valueFlag(&a.tags, "tag", "Tag to apply to the snapshot (can be specified multiple times)",
			withPlaceholder("<tag>"), asRepeatable()),
		valueFlag(&a.excludes, "exclude", "Exclude pattern (gitignore syntax, repeatable)",
			withPlaceholder("<pattern>"), asRepeatable()),
	}}
}

func runBackup(r *runner, ctx context.Context, a *backupArgs) int {
	if a.profile != "" && a.allProfiles {
		return r.fail("-profile and -all-profiles are mutually exclusive")
	}

	if a.profile != "" || a.allProfiles {
		return runBackupWithProfiles(r, ctx, a)
	}

	if a.authRef != "" {
		cfg, err := profile.Load(a.profilesFile)
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
		sourceSFTP:        sourceSFTPConfigFromFlags(a.globalFlags),
		excludePatterns:   excludePatterns,
	})
	if err != nil {
		return r.fail("Failed to init source: %v", err)
	}

	if err := r.openClient(ctx, a.globalFlags); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	backupOpts := buildBackupOpts(a, excludePatterns)

	result, err := r.client.Backup(ctx, src, backupOpts...)
	if err != nil {
		return r.fail("Backup failed: %v", err)
	}
	if a.jsonEnabled() {
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

	switch uri.Scheme {
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

		cfg, loadErr := loadProfilesOrInit(a.profilesFile)
		if loadErr != nil {
			return fmt.Errorf("load profiles for default auth: %w", loadErr)
		}
		ensureProfilesMaps(cfg)

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

		if saveErr := profile.Save(a.profilesFile, cfg); saveErr != nil {
			return fmt.Errorf("save profiles with default auth: %w", saveErr)
		}

		if err := applyProfileAuthToBackupArgs(a, auth); err != nil {
			return err
		}
	}

	return nil
}

func runBackupWithProfiles(r *runner, ctx context.Context, base *backupArgs) int {
	cfg, err := profile.Load(base.profilesFile)
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
			r.fail("[%s] profile merge failed: %v", name, err)
			failures++
			continue
		}
		if !base.jsonEnabled() {
			_, _ = fmt.Fprintf(r.out, "\n== Running profile %s ==\n", name)
		}
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

// mergeProfileBackupArgs folds profile p's fields into a copy of base,
// wherever the corresponding flag was not passed explicitly. It is the
// backup-specific counterpart to config.go's applyProfileStore: that function
// covers a profile's store, folded in later via g.profile when
// resolveClientConfig runs; this one covers everything else a backup profile
// can carry (source, tags, excludes, native-source credentials, auth). Both
// apply the same "explicit flag beats profile value" precedence, just against
// different field shapes.
func mergeProfileBackupArgs(base *backupArgs, profileName string, p profile.Profile, cfg *profile.Config) (*backupArgs, error) {
	g := cloneGlobalFlags(base.globalFlags)
	a := *base
	a.globalFlags = g

	if !a.flagProvided("source") {
		a.sourceURI = p.Source
	}
	if a.sourceURI == "" {
		return nil, fmt.Errorf("profile %q has empty source", profileName)
	}

	if !a.flagProvided("skip-native-files") {
		a.skipNativeFiles = p.SkipNativeFiles
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
	if !a.flagProvided("exclude-file") && p.ExcludeFile != "" {
		a.excludeFile = p.ExcludeFile
	}
	if !a.flagProvided("ignore-empty-snapshot") {
		a.ignoreEmpty = p.IgnoreEmpty
	}

	// The store itself is not folded in here. Naming the profile is enough:
	// resolveClientConfig looks it up when the client is opened, so the store
	// definition is applied in exactly one place and parsed flags stay intact.
	// lookupProfileStore (config.go) is the same check loadProfileStore
	// performs at that point; it's repeated here only to fail fast, before
	// spending effort on a backup that would fail at store-open time anyway.
	if _, err := lookupProfileStore(cfg, profileName, p); err != nil {
		return nil, err
	}
	g.profile = profileName

	if p.AuthRef != "" {
		effectiveAuthRef := p.AuthRef
		if a.flagProvided("auth-ref") {
			effectiveAuthRef = a.authRef
		}
		authCfg, ok := cfg.Auth[effectiveAuthRef]
		if !ok {
			return nil, fmt.Errorf("profile %q references unknown auth %q", profileName, effectiveAuthRef)
		}
		if err := applyProfileAuthToBackupArgs(&a, authCfg); err != nil {
			return nil, fmt.Errorf("profile %q auth %q: %w", profileName, effectiveAuthRef, err)
		}
	} else if a.flagProvided("auth-ref") {
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

func applyProfileAuthToBackupArgs(a *backupArgs, auth profile.Auth) error {
	uri, err := parseSourceURI(a.sourceURI)
	if err != nil {
		return fmt.Errorf("parse source URI: %w", err)
	}

	requiredProvider := ""
	switch uri.Scheme {
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
		if !a.flagProvided("google-credentials") && auth.GoogleCreds != "" {
			a.googleCreds = auth.GoogleCreds
		}
		if !a.flagProvided("google-credentials-ref") && auth.GoogleCredsRef != "" {
			a.googleCredsRef = auth.GoogleCredsRef
		}
		if !a.flagProvided("google-credentials-json") && auth.GoogleCredsJSON != "" {
			a.googleCredsJSON = auth.GoogleCredsJSON
		}
		if !a.flagProvided("google-token-file") && auth.GoogleTokenFile != "" {
			a.googleTokenFile = auth.GoogleTokenFile
		}
		if !a.flagProvided("google-token-ref") && auth.GoogleTokenRef != "" {
			a.googleTokenRef = auth.GoogleTokenRef
		}
	}

	if requiredProvider == "onedrive" {
		if !a.flagProvided("onedrive-client-id") && auth.OneDriveClientID != "" {
			a.onedriveClientID = auth.OneDriveClientID
		}
		if !a.flagProvided("onedrive-token-file") && auth.OneDriveTokenFile != "" {
			a.onedriveTokenFile = auth.OneDriveTokenFile
		}
		if !a.flagProvided("onedrive-token-ref") && auth.OneDriveTokenRef != "" {
			a.onedriveTokenRef = auth.OneDriveTokenRef
		}
	}

	return nil
}

// cloneGlobalFlags returns an independent copy of src: globalFlags holds only
// value fields (plus the shared *ui.SafeLogWriter debug log), so a shallow
// struct copy is a complete, correct clone.
func cloneGlobalFlags(src *globalFlags) *globalFlags {
	clone := *src
	return &clone
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
	if a.verbose {
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
	sourceSFTP        sftpConfig
	excludePatterns   []string
}

func initSource(ctx context.Context, opts initSourceOptions) (source.Source, error) {
	uri, err := parseSourceURI(opts.sourceURI)
	if err != nil {
		return nil, err
	}

	resolver := secretrefbackends.NewDefaultResolver()

	switch uri.Scheme {
	case "local":
		localOpts := []local.Option{local.WithExcludePatterns(opts.excludePatterns)}
		if opts.volumeUUID != "" {
			localOpts = append(localOpts, local.WithVolumeUUID(opts.volumeUUID))
		}
		if opts.skipMode {
			localOpts = append(localOpts, local.WithSkipMode())
		}
		if opts.skipFlags {
			localOpts = append(localOpts, local.WithSkipFlags())
		}
		if opts.skipXattrs {
			localOpts = append(localOpts, local.WithSkipXattrs())
		}
		if opts.xattrNamespaces != "" {
			prefixes := parseXattrNamespacePrefixes(opts.xattrNamespaces)
			if len(prefixes) > 0 {
				localOpts = append(localOpts, local.WithXattrNamespaces(prefixes))
			}
		}
		return local.New(uri.Path, localOpts...), nil
	case "sftp":
		sftpOpts := sftpSourceOpts(opts.sourceSFTP, uri)
		sftpOpts = append(sftpOpts, sftpsource.WithExcludePatterns(opts.excludePatterns))
		return sftpsource.New(uri.Host, sftpOpts...)
	case "gdrive":
		tokenPath, err := resolveTokenPath(opts.googleTokenFile, "google_token.json")
		if err != nil {
			return nil, err
		}
		gdriveOpts := []gdrive.Option{
			gdrive.WithResolver(resolver),
			gdrive.WithCredsPath(opts.googleCreds),
			gdrive.WithCredsRef(opts.googleCredsRef),
			gdrive.WithCredsJSON([]byte(opts.googleCredsJSON)),
			gdrive.WithTokenPath(tokenPath),
			gdrive.WithTokenRef(opts.googleTokenRef),
			gdrive.WithDriveName(uri.Host),
			gdrive.WithRootPath(uri.Path),
			gdrive.WithExcludePatterns(opts.excludePatterns),
		}
		if opts.skipNativeFiles {
			gdriveOpts = append(gdriveOpts, gdrive.WithSkipNativeFiles())
		}
		return gdrive.New(ctx, gdriveOpts...)
	case "gdrive-changes":
		tokenPath, err := resolveTokenPath(opts.googleTokenFile, "google_token.json")
		if err != nil {
			return nil, err
		}
		gdriveOpts := []gdrive.Option{
			gdrive.WithResolver(resolver),
			gdrive.WithCredsPath(opts.googleCreds),
			gdrive.WithCredsRef(opts.googleCredsRef),
			gdrive.WithCredsJSON([]byte(opts.googleCredsJSON)),
			gdrive.WithTokenPath(tokenPath),
			gdrive.WithTokenRef(opts.googleTokenRef),
			gdrive.WithDriveName(uri.Host),
			gdrive.WithRootPath(uri.Path),
			gdrive.WithExcludePatterns(opts.excludePatterns),
		}
		if opts.skipNativeFiles {
			gdriveOpts = append(gdriveOpts, gdrive.WithSkipNativeFiles())
		}
		return gdrive.NewChangeSource(ctx, gdriveOpts...)
	case "onedrive":
		tokenPath, err := resolveTokenPath(opts.onedriveTokenFile, "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return onedrive.New(ctx,
			onedrive.WithResolver(resolver),
			onedrive.WithClientID(opts.onedriveClientID),
			onedrive.WithTokenPath(tokenPath),
			onedrive.WithTokenRef(opts.onedriveTokenRef),
			onedrive.WithDriveName(uri.Host),
			onedrive.WithRootPath(uri.Path),
			onedrive.WithExcludePatterns(opts.excludePatterns),
		)
	case "onedrive-changes":
		tokenPath, err := resolveTokenPath(opts.onedriveTokenFile, "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return onedrive.NewChangeSource(ctx,
			onedrive.WithResolver(resolver),
			onedrive.WithClientID(opts.onedriveClientID),
			onedrive.WithTokenPath(tokenPath),
			onedrive.WithTokenRef(opts.onedriveTokenRef),
			onedrive.WithDriveName(uri.Host),
			onedrive.WithRootPath(uri.Path),
			onedrive.WithExcludePatterns(opts.excludePatterns),
		)
	default:
		return nil, fmt.Errorf("unsupported source: %s", uri.Scheme)
	}
}

func sftpSourceOpts(cfg sftpConfig, uri *sourceURIParts) []sftpsource.Option {
	opts := []sftpsource.Option{
		sftpsource.WithBasePath(uri.Path),
	}
	if uri.Port != "" {
		opts = append(opts, sftpsource.WithPort(uri.Port))
	}
	if uri.User != "" {
		opts = append(opts, sftpsource.WithUser(uri.User))
	}
	if cfg.Password != "" {
		opts = append(opts, sftpsource.WithPassword(cfg.Password))
	}
	if cfg.Key != "" {
		opts = append(opts, sftpsource.WithKey(cfg.Key))
	}
	if cfg.Insecure {
		opts = append(opts, sftpsource.WithHostKeyCallback(ssh.InsecureIgnoreHostKey())) //nolint:gosec // explicitly requested by user
	}
	if cfg.KnownHosts != "" {
		opts = append(opts, sftpsource.WithKnownHosts(cfg.KnownHosts))
	}
	return opts
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

// backupCommand declares the `backup` command.
func backupCommand() command {
	return leaf("backup", "Create a new backup snapshot from a source",
		backupCommandGroups, declareBackupArgs, runBackup)
}
