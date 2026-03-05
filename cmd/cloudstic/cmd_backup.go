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
	g           *globalFlags
	sourceType  string
	sourcePath  string
	driveID     string
	rootFolder  string
	dryRun      bool
	excludeFile string
	tags        stringArrayFlags
	excludes    stringArrayFlags
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
	excludeFile := fs.String("exclude-file", "", "Path to file with exclude patterns (one per line, gitignore syntax)")
	fs.Var(&a.tags, "tag", "Tag to apply to the snapshot (can be specified multiple times)")
	fs.Var(&a.excludes, "exclude", "Exclude pattern (gitignore syntax, repeatable)")
	mustParse(fs)
	a.sourceType = *sourceType
	a.sourcePath = *sourcePath
	a.driveID = *driveID
	a.rootFolder = *rootFolder
	a.dryRun = *dryRun
	a.excludeFile = *excludeFile
	return a
}

func runBackup() {
	a := parseBackupArgs()

	// Collect exclude patterns from -exclude flags and -exclude-file.
	excludePatterns := []string(a.excludes)
	if a.excludeFile != "" {
		filePatterns, err := source.ParseExcludeFile(a.excludeFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read exclude file: %v\n", err)
			os.Exit(1)
		}
		excludePatterns = append(excludePatterns, filePatterns...)
	}

	ctx := context.Background()

	src, err := initSource(ctx, a.sourceType, a.sourcePath, a.driveID, a.rootFolder, a.g, excludePatterns)
	if err != nil {
		fmt.Printf("Failed to init source: %v\n", err)
		os.Exit(1)
	}

	client, err := a.g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	var backupOpts []cloudstic.BackupOption
	if *a.g.verbose {
		backupOpts = append(backupOpts, cloudstic.WithVerbose())
	}
	if a.dryRun {
		backupOpts = append(backupOpts, engine.WithBackupDryRun())
	}
	if len(a.tags) > 0 {
		backupOpts = append(backupOpts, cloudstic.WithTags(a.tags...))
	}
	if len(excludePatterns) > 0 {
		h := sha256.Sum256([]byte(strings.Join(excludePatterns, "\n")))
		backupOpts = append(backupOpts, cloudstic.WithExcludeHash(hex.EncodeToString(h[:])))
	}
	result, err := client.Backup(ctx, src, backupOpts...)
	if err != nil {
		fmt.Printf("Backup failed: %v\n", err)
		os.Exit(1)
	}
	printBackupSummary(result)
}

func printBackupSummary(r *engine.RunResult) {
	total := r.FilesNew + r.FilesChanged + r.FilesUnmodified +
		r.DirsNew + r.DirsChanged + r.DirsUnmodified
	if r.DryRun {
		fmt.Printf("\nBackup dry run complete.\n")
	} else {
		fmt.Printf("\nBackup complete. Snapshot: %s, Root: %s\n", r.SnapshotRef, r.Root)
	}
	fmt.Printf("Files:  %d new,  %d changed,  %d unmodified,  %d removed\n",
		r.FilesNew, r.FilesChanged, r.FilesUnmodified, r.FilesRemoved)
	fmt.Printf("Dirs:   %d new,  %d changed,  %d unmodified,  %d removed\n",
		r.DirsNew, r.DirsChanged, r.DirsUnmodified, r.DirsRemoved)
	if !r.DryRun {
		fmt.Printf("Added to the repository: %s (%s compressed)\n",
			formatBytes(r.BytesAddedRaw), formatBytes(r.BytesAddedStored))
	}
	fmt.Printf("Processed %d entries in %s\n",
		total, r.Duration.Round(time.Second))
	if !r.DryRun {
		fmt.Printf("Snapshot %s saved\n", r.SnapshotHash)
	}
}

func initSource(ctx context.Context, sourceType, sourcePath, driveID, rootFolder string, g *globalFlags, excludePatterns []string) (source.Source, error) {
	switch sourceType {
	case "local":
		return source.NewLocalSource(sourcePath, source.WithLocalExcludePatterns(excludePatterns)), nil
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
		return source.NewGDriveSource(
			ctx,
			source.WithCredsPath(creds),
			source.WithTokenPath(tokenPath),
			source.WithDriveID(driveID),
			source.WithRootFolderID(rootFolder),
			source.WithGDriveExcludePatterns(excludePatterns),
		)
	case "gdrive-changes":
		creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("GOOGLE_TOKEN_FILE", "google_token.json")
		if err != nil {
			return nil, err
		}
		return source.NewGDriveChangeSource(
			ctx,
			source.WithCredsPath(creds),
			source.WithTokenPath(tokenPath),
			source.WithDriveID(driveID),
			source.WithRootFolderID(rootFolder),
			source.WithGDriveExcludePatterns(excludePatterns),
		)
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
