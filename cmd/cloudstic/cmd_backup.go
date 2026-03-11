package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/paths"
	"github.com/cloudstic/cli/pkg/source"
)

type backupArgs struct {
	g               *globalFlags
	sourceType      string
	sourcePath      string
	driveID         string
	rootFolder      string
	dryRun          bool
	excludeFile     string
	skipNativeFiles bool
	volumeUUID      string
	tags            stringArrayFlags
	excludes        stringArrayFlags
}

func parseBackupArgs() *backupArgs {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	a := &backupArgs{}
	a.g = addGlobalFlags(fs)
	sourceType := fs.String("source", envDefault("CLOUDSTIC_SOURCE", "gdrive"), "source type (gdrive, gdrive-changes, local, onedrive, onedrive-changes)")
	sourcePath := fs.String("source-path", envDefault("CLOUDSTIC_SOURCE_PATH", "."), "Local source path (if source=local)")
	driveID := fs.String("drive-id", envDefault("CLOUDSTIC_DRIVE_ID", ""), "Shared drive ID for gdrive source (omit for My Drive)")
	rootFolder := fs.String("root-folder", envDefault("CLOUDSTIC_ROOT_FOLDER", ""), "Root folder ID for gdrive source (defaults to entire drive)")
	dryRun := fs.Bool("dry-run", false, "Scan source and report changes without writing to the store")
	skipNativeFiles := fs.Bool("skip-native-files", false, "Exclude Google-native files (Docs, Sheets, Slides, etc.) from the backup")
	excludeFile := fs.String("exclude-file", "", "Path to file with exclude patterns (one per line, gitignore syntax)")
	volumeUUID := fs.String("volume-uuid", envDefault("CLOUDSTIC_VOLUME_UUID", ""), "Override volume UUID for local source (enables cross-machine incremental backup)")
	fs.Var(&a.tags, "tag", "Tag to apply to the snapshot (can be specified multiple times)")
	fs.Var(&a.excludes, "exclude", "Exclude pattern (gitignore syntax, repeatable)")
	mustParse(fs)
	a.sourceType = *sourceType
	a.sourcePath = *sourcePath
	a.driveID = *driveID
	a.rootFolder = *rootFolder
	a.dryRun = *dryRun
	a.skipNativeFiles = *skipNativeFiles
	a.excludeFile = *excludeFile
	a.volumeUUID = *volumeUUID
	return a
}

func (r *runner) runBackup() int {
	a := parseBackupArgs()

	excludePatterns, err := r.parseExcludePatterns(a)
	if err != nil {
		return r.fail("Failed to read exclude file: %v", err)
	}

	ctx := context.Background()

	src, err := initSource(ctx, a.sourceType, a.sourcePath, a.driveID, a.rootFolder, a.skipNativeFiles, a.volumeUUID, a.g, excludePatterns)
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

func initSource(ctx context.Context, sourceType, sourcePath, driveID, rootFolder string, skipNativeFiles bool, volumeUUID string, g *globalFlags, excludePatterns []string) (source.Source, error) {
	switch sourceType {
	case "local":
		opts := []source.LocalOption{source.WithLocalExcludePatterns(excludePatterns)}
		if volumeUUID != "" {
			opts = append(opts, source.WithVolumeUUID(volumeUUID))
		}
		return source.NewLocalSource(sourcePath, opts...), nil
	case "sftp":
		sftpHost, sftpOpts := g.sftpSourceOpts(g.sourceSFTPHost, g.sourceSFTPPort, g.sourceSFTPUser, g.sourceSFTPPassword, g.sourceSFTPKey, &sourcePath)
		if sftpHost == "" {
			return nil, fmt.Errorf("--sftp-host is required for sftp source")
		}
		if sourcePath == "" {
			return nil, fmt.Errorf("-source-path is required for sftp source")
		}
		sftpOpts = append(sftpOpts, source.WithSFTPExcludePatterns(excludePatterns))
		return source.NewSFTPSource(sftpHost, sftpOpts...)
	case "gdrive":
		creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("GOOGLE_TOKEN_FILE", "google_token.json")
		if err != nil {
			return nil, err
		}
		gdriveOpts := []source.GDriveOption{
			source.WithCredsPath(creds),
			source.WithTokenPath(tokenPath),
			source.WithDriveID(driveID),
			source.WithRootFolderID(rootFolder),
			source.WithGDriveExcludePatterns(excludePatterns),
		}
		if skipNativeFiles {
			gdriveOpts = append(gdriveOpts, source.WithSkipNativeFiles())
		}
		return source.NewGDriveSource(ctx, gdriveOpts...)
	case "gdrive-changes":
		creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("GOOGLE_TOKEN_FILE", "google_token.json")
		if err != nil {
			return nil, err
		}
		gdriveOpts := []source.GDriveOption{
			source.WithCredsPath(creds),
			source.WithTokenPath(tokenPath),
			source.WithDriveID(driveID),
			source.WithRootFolderID(rootFolder),
			source.WithGDriveExcludePatterns(excludePatterns),
		}
		if skipNativeFiles {
			gdriveOpts = append(gdriveOpts, source.WithSkipNativeFiles())
		}
		return source.NewGDriveChangeSource(ctx, gdriveOpts...)
	case "onedrive":
		clientID := os.Getenv("ONEDRIVE_CLIENT_ID") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("ONEDRIVE_TOKEN_FILE", "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return source.NewOneDriveSource(ctx,
			source.WithOneDriveClientID(clientID),
			source.WithOneDriveTokenPath(tokenPath),
			source.WithOneDriveExcludePatterns(excludePatterns),
		)
	case "onedrive-changes":
		clientID := os.Getenv("ONEDRIVE_CLIENT_ID") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("ONEDRIVE_TOKEN_FILE", "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return source.NewOneDriveChangeSource(ctx,
			source.WithOneDriveClientID(clientID),
			source.WithOneDriveTokenPath(tokenPath),
			source.WithOneDriveExcludePatterns(excludePatterns),
		)
	default:
		return nil, fmt.Errorf("unsupported source type: %s", sourceType)
	}
}

// resolveTokenPath returns an absolute path for a token file. If the
// environment variable envKey is set, that value is used as-is. Otherwise
// the filename is placed inside the cloudstic config directory.
func resolveTokenPath(envKey, defaultFilename string) (string, error) {
	if v := os.Getenv(envKey); v != "" {
		return v, nil
	}
	return paths.TokenPath(defaultFilename)
}
