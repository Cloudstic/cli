package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
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
}

func parseBackupArgs() *backupArgs {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	a := &backupArgs{}
	a.g = addGlobalFlags(fs)
	sourceURI := fs.String("source", envDefault("CLOUDSTIC_SOURCE", "gdrive"), "Source URI: local:<path>, sftp://[user@]host[:port]/<path>, gdrive[://<Drive Name>][/<path>], gdrive-changes[://<Drive Name>][/<path>], onedrive[://<Drive Name>][/<path>], onedrive-changes[://<Drive Name>][/<path>]")
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
	a.dryRun = *dryRun
	a.skipNativeFiles = *skipNativeFiles
	a.excludeFile = *excludeFile
	a.volumeUUID = *volumeUUID
	a.googleCreds = *googleCreds
	a.googleTokenFile = *googleTokenFile
	a.onedriveClientID = *onedriveClientID
	a.onedriveTokenFile = *onedriveTokenFile
	return a
}

func (r *runner) runBackup() int {
	a := parseBackupArgs()

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
