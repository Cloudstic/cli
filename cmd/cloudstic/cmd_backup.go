package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/open"

	"github.com/cloudstic/cli/pkg/profile"

	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/paths"
	// Aliased to keep "sftp" free: this file also reaches SSH host-key types,
	// and the store-side SFTP wiring lives alongside it.
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

	bcfg := backupConfigFromFlags(a)

	if a.authRef != "" {
		cfg, err := profile.Load(a.profilesFile)
		if err != nil {
			return r.fail("Failed to load profiles: %v", err)
		}
		authCfg, ok := cfg.Auth[a.authRef]
		if !ok {
			return r.fail("Unknown auth reference %q", a.authRef)
		}
		// The flags win over the entry they name — a user who passed both meant the
		// flag, so the entry only fills what was left unsaid.
		if bcfg, err = config.ApplyProfileAuth(bcfg, backupDecidedFields(a), authCfg); err != nil {
			return r.fail("Auth reference %q: %v", a.authRef, err)
		}
	}

	bcfg, err := ensureDefaultAuthRef(bcfg, a.profilesFile)
	if err != nil {
		return r.fail("Failed to prepare auth settings: %v", err)
	}
	return execBackup(r, ctx, a, bcfg)
}

// execBackup opens the source and store described by bcfg and runs the backup.
//
// a is still needed for two things the resolved backup configuration
// deliberately does not carry: the store half of the configuration, which
// r.openClient resolves from the flags and the selected profile, and the output
// mode.
func execBackup(r *runner, ctx context.Context, a *backupArgs, bcfg config.Backup) int {
	job, err := open.Backup(ctx, bcfg,
		open.WithSecretResolver(newSecretResolver(bcfg.Source.ConfigDir)),
		open.WithPromptWriter(r.errOut))
	if err != nil {
		return r.fail("Failed to init source: %v", err)
	}

	cfg, err := resolveClientConfig(a.globalFlags)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}
	if err := r.openClient(ctx, cfg); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	result, err := r.client.Backup(ctx, job.Source, job.Options...)
	if err != nil {
		return r.fail("Backup failed: %v", err)
	}
	if a.jsonEnabled() {
		return r.writeJSON(result)
	}
	printBackupSummary(r.out, result)
	return 0
}

// backupConfigFromFlags projects parsed flags into a resolved backup
// configuration. It is the counterpart to clientConfigFromFlags (config.go) and
// is likewise a pure translation: no I/O, no mutation of a.
func backupConfigFromFlags(a *backupArgs) config.Backup {
	return config.Backup{
		Source: config.Source{
			URI:             a.sourceURI,
			ConfigDir:       a.configDir,
			SFTP:            sourceSFTPConfigFromFlags(a.globalFlags),
			Google:          config.Google{CredsPath: a.googleCreds, CredsRef: a.googleCredsRef, CredsJSON: a.googleCredsJSON, TokenPath: a.googleTokenFile, TokenRef: a.googleTokenRef},
			OneDrive:        config.OneDrive{ClientID: a.onedriveClientID, TokenPath: a.onedriveTokenFile, TokenRef: a.onedriveTokenRef},
			VolumeUUID:      a.volumeUUID,
			SkipNativeFiles: a.skipNativeFiles,
			SkipMode:        a.skipMode,
			SkipFlags:       a.skipFlags,
			SkipXattrs:      a.skipXattrs,
			XattrNamespaces: splitCommaList(a.xattrNamespaces),
			Excludes:        []string(a.excludes),
			ExcludeFile:     a.excludeFile,
		},
		Tags:        []string(a.tags),
		IgnoreEmpty: a.ignoreEmpty,
		AuthRef:     a.authRef,
		DryRun:      a.dryRun,
		Verbose:     a.verbose,
	}
}

// backupDecidedFields is the set of profile-supplied backup fields the user
// settled on the command line, derived from config.BackupFields for the same
// reason flagDecidedFields is (see config.go).
//
// The repeatable flags are excluded: -tag and -exclude merge on emptiness
// rather than on being passed, so naming them here would change that rule.
func backupDecidedFields(a *backupArgs) config.FieldSet {
	decided := config.NewFieldSet()
	for _, f := range config.BackupFields() {
		switch f {
		case config.FieldTags, config.FieldExcludes:
			continue
		}
		if a.flagProvided(string(f)) {
			decided[f] = struct{}{}
		}
	}
	return decided
}

// ensureDefaultAuthRef gives a cloud backup an auth entry to use when the user
// named none, creating it in the profiles file on first use.
//
// This is onboarding, not resolution: `cloudstic backup gdrive` with no further
// configuration should work a second time without re-authenticating, which means
// persisting where the token went. A non-cloud source needs none of this and is
// returned unchanged.
func ensureDefaultAuthRef(bcfg config.Backup, profilesFile string) (config.Backup, error) {
	uri, err := config.ParseSourceURI(bcfg.Source.URI)
	if err != nil {
		return config.Backup{}, err
	}

	var provider, authRef, tokenFilename string
	switch uri.Scheme {
	case "gdrive", "gdrive-changes":
		provider, authRef, tokenFilename = "google", "google-default", "google_token.json"
	case "onedrive", "onedrive-changes":
		provider, authRef, tokenFilename = "onedrive", "onedrive-default", "onedrive_token.json"
	default:
		return bcfg, nil
	}
	if bcfg.AuthRef != "" {
		return bcfg, nil
	}

	cfg, err := loadProfilesOrInit(profilesFile)
	if err != nil {
		return config.Backup{}, fmt.Errorf("load profiles for default auth: %w", err)
	}
	ensureProfilesMaps(cfg)

	auth := cfg.Auth[authRef]
	if auth.Provider != "" && auth.Provider != provider {
		return config.Backup{}, fmt.Errorf("default auth %q has provider %q, expected %q", authRef, auth.Provider, provider)
	}
	auth.Provider = provider

	// Record the credentials the user passed on the command line, so the next
	// run finds them without being told again.
	switch provider {
	case "google":
		tokenPath, err := defaultTokenPath(bcfg.Source.ConfigDir, bcfg.Source.Google.TokenPath, tokenFilename)
		if err != nil {
			return config.Backup{}, err
		}
		if bcfg.Source.Google.CredsPath != "" {
			auth.GoogleCreds = bcfg.Source.Google.CredsPath
		}
		if bcfg.Source.Google.CredsJSON != "" {
			auth.GoogleCredsJSON = bcfg.Source.Google.CredsJSON
		}
		auth.GoogleTokenFile = tokenPath
	case "onedrive":
		tokenPath, err := defaultTokenPath(bcfg.Source.ConfigDir, bcfg.Source.OneDrive.TokenPath, tokenFilename)
		if err != nil {
			return config.Backup{}, err
		}
		if bcfg.Source.OneDrive.ClientID != "" {
			auth.OneDriveClientID = bcfg.Source.OneDrive.ClientID
		}
		auth.OneDriveTokenFile = tokenPath
	}
	cfg.Auth[authRef] = auth

	if err := profile.Save(profilesFile, cfg); err != nil {
		return config.Backup{}, fmt.Errorf("save profiles with default auth: %w", err)
	}

	bcfg.AuthRef = authRef
	// Nothing is decided here: the entry was just written from this very
	// configuration, so folding it back in is what fills the token path.
	return config.ApplyProfileAuth(bcfg, nil, auth)
}

// defaultTokenPath mirrors open.Source's token-path rule so the entry recorded
// in the profiles file names the file the source will actually use.
func defaultTokenPath(configDir, explicit, filename string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return paths.TokenPath(configDir, filename)
}

func runBackupWithProfiles(r *runner, ctx context.Context, base *backupArgs) int {
	cfg, err := profile.Load(base.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}

	names, err := backupProfileNames(cfg, base)
	if err != nil {
		return r.fail("Failed to select profiles: %v", err)
	}

	fromFlags := backupConfigFromFlags(base)
	decided := backupDecidedFields(base)

	failures := 0
	for _, name := range names {
		bcfg, err := config.MergeProfileBackup(fromFlags, decided, name, cfg)
		if err != nil {
			r.fail("[%s] profile merge failed: %v", name, err)
			failures++
			continue
		}
		// Fail before spending effort on a backup whose store cannot be found.
		// The store itself is applied where every other command applies it, when
		// the client is opened below (see resolveClientConfig).
		if _, err := cfg.StoreFor(name); err != nil {
			r.fail("[%s] profile merge failed: %v", name, err)
			failures++
			continue
		}

		if !base.jsonEnabled() {
			_, _ = fmt.Fprintf(r.out, "\n== Running profile %s ==\n", name)
		}

		// Naming the profile is how the store half reaches resolveClientConfig;
		// the parsed flags are left intact so their precedence still applies.
		perProfile := *base
		g := *base.globalFlags
		g.profile = name
		perProfile.globalFlags = &g

		r.client = nil // each profile may target a different store
		if code := execBackup(r, ctx, &perProfile, bcfg); code != 0 {
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

// backupProfileNames resolves which profiles this invocation runs: the one named
// by -profile, or every enabled profile for -all-profiles.
func backupProfileNames(cfg *profile.Config, a *backupArgs) ([]string, error) {
	if a.profile != "" {
		if _, ok := cfg.Profiles[a.profile]; !ok {
			return nil, fmt.Errorf("unknown profile %q", a.profile)
		}
		return []string{a.profile}, nil
	}

	var names []string
	for name, p := range cfg.Profiles {
		if p.IsEnabled() {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no enabled profiles found")
	}
	slices.Sort(names)
	return names, nil
}

// splitCommaList splits a comma-separated flag value, dropping blanks.
func splitCommaList(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// backupCommand declares the `backup` command.
func backupCommand() command {
	return leaf("backup", "Create a new backup snapshot from a source",
		backupCommandGroups, declareBackupArgs, runBackup)
}
