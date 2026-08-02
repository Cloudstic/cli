package open

import (
	"context"
	"fmt"

	"golang.org/x/crypto/ssh"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/paths"
	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/source"
	"github.com/cloudstic/cli/pkg/source/gdrive"
	"github.com/cloudstic/cli/pkg/source/local"
	"github.com/cloudstic/cli/pkg/source/onedrive"

	// Aliased to keep "sftp" free: this file also reaches SSH host-key types.
	sftpsource "github.com/cloudstic/cli/pkg/source/sftp"
)

// Source constructs the backup source described by cfg.
//
// The result is ready to hand to Client.Backup. Exclude patterns are applied by
// the source itself as it walks, so a caller building a source directly gets the
// same filtering a backup would — but see Backup, which additionally reads
// cfg.ExcludeFile and derives the snapshot's exclude hash from the same list.
//
// Secret references in the credentials are resolved by the source when it
// authenticates, through the resolver WithSecretResolver names.
func Source(ctx context.Context, cfg config.Source, opts ...Option) (source.Source, error) {
	return openSource(ctx, cfg, newOptions(opts))
}

func openSource(ctx context.Context, cfg config.Source, o *options) (source.Source, error) {
	uri, err := config.ParseSourceURI(cfg.URI)
	if err != nil {
		return nil, err
	}

	switch uri.Scheme {
	case "local":
		return local.New(uri.Path, localOpts(cfg)...), nil
	case "sftp":
		sftpOpts := append(sftpSourceOpts(cfg.SFTP, uri),
			sftpsource.WithExcludePatterns(cfg.Excludes))
		return sftpsource.New(uri.Host, sftpOpts...)
	case "gdrive", "gdrive-changes":
		gdriveOpts, err := googleOpts(cfg, uri, o)
		if err != nil {
			return nil, err
		}
		if uri.Scheme == "gdrive" {
			return gdrive.New(ctx, gdriveOpts...)
		}
		return gdrive.NewChangeSource(ctx, gdriveOpts...)
	case "onedrive", "onedrive-changes":
		onedriveOpts, err := oneDriveOpts(cfg, uri, o)
		if err != nil {
			return nil, err
		}
		if uri.Scheme == "onedrive" {
			return onedrive.New(ctx, onedriveOpts...)
		}
		return onedrive.NewChangeSource(ctx, onedriveOpts...)
	default:
		// Unreachable for the same reason as the store switch in open.go:
		// ParseSourceURI yields one of these schemes or an error.
		return nil, fmt.Errorf("internal error: source URI %q parsed to unhandled scheme %q", cfg.URI, uri.Scheme)
	}
}

func localOpts(cfg config.Source) []local.Option {
	opts := []local.Option{local.WithExcludePatterns(cfg.Excludes)}
	if cfg.VolumeUUID != "" {
		opts = append(opts, local.WithVolumeUUID(cfg.VolumeUUID))
	}
	if cfg.SkipMode {
		opts = append(opts, local.WithSkipMode())
	}
	if cfg.SkipFlags {
		opts = append(opts, local.WithSkipFlags())
	}
	if cfg.SkipXattrs {
		opts = append(opts, local.WithSkipXattrs())
	}
	if len(cfg.XattrNamespaces) > 0 {
		opts = append(opts, local.WithXattrNamespaces(cfg.XattrNamespaces))
	}
	return opts
}

// googleOpts builds the Drive options shared by the full and incremental
// sources, which differ only in the constructor they are passed to.
func googleOpts(cfg config.Source, uri *config.SourceURI, o *options) ([]gdrive.Option, error) {
	tokenPath, err := tokenPath(cfg.ConfigDir, cfg.Google.TokenPath, "google_token.json")
	if err != nil {
		return nil, err
	}
	opts := []gdrive.Option{
		gdrive.WithResolver(o.resolver()),
		gdrive.WithCredsPath(cfg.Google.CredsPath),
		gdrive.WithCredsRef(cfg.Google.CredsRef),
		gdrive.WithCredsJSON([]byte(cfg.Google.CredsJSON)),
		gdrive.WithTokenPath(tokenPath),
		gdrive.WithTokenRef(cfg.Google.TokenRef),
		gdrive.WithDriveName(uri.Host),
		gdrive.WithRootPath(uri.Path),
		gdrive.WithExcludePatterns(cfg.Excludes),
		gdrive.WithPromptWriter(o.promptWriter),
	}
	if cfg.SkipNativeFiles {
		opts = append(opts, gdrive.WithSkipNativeFiles())
	}
	return opts, nil
}

func oneDriveOpts(cfg config.Source, uri *config.SourceURI, o *options) ([]onedrive.Option, error) {
	tokenPath, err := tokenPath(cfg.ConfigDir, cfg.OneDrive.TokenPath, "onedrive_token.json")
	if err != nil {
		return nil, err
	}
	return []onedrive.Option{
		onedrive.WithResolver(o.resolver()),
		onedrive.WithClientID(cfg.OneDrive.ClientID),
		onedrive.WithTokenPath(tokenPath),
		onedrive.WithTokenRef(cfg.OneDrive.TokenRef),
		onedrive.WithDriveName(uri.Host),
		onedrive.WithRootPath(uri.Path),
		onedrive.WithExcludePatterns(cfg.Excludes),
		onedrive.WithPromptWriter(o.promptWriter),
	}, nil
}

func sftpSourceOpts(cfg config.SFTP, uri *config.SourceURI) []sftpsource.Option {
	opts := []sftpsource.Option{sftpsource.WithBasePath(uri.Path)}
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
		opts = append(opts, sftpsource.WithHostKeyCallback(ssh.InsecureIgnoreHostKey())) //nolint:gosec // explicitly requested by the caller
	}
	if cfg.KnownHosts != "" {
		opts = append(opts, sftpsource.WithKnownHosts(cfg.KnownHosts))
	}
	return opts
}

// tokenPath returns where a cloud source's OAuth token lives: the explicit path
// when one is given, and otherwise defaultName inside the config directory.
func tokenPath(configDir, explicit, defaultName string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return paths.TokenPath(configDir, defaultName)
}

// BackupJob is a source paired with the options that describe how to record what
// it yields.
//
// The two travel together because they share one derived value: the snapshot's
// exclude hash must be the hash of exactly the patterns the source is filtering
// on. Returning them separately, or deriving the hash at a second call site,
// is how they come to disagree — and the engine reads a mismatch as "the
// exclude patterns changed", forcing a full rescan (or, worse, missing one).
type BackupJob struct {
	Source  source.Source
	Options []cloudstic.BackupOption
}

// Backup constructs the source and options for one backup run.
//
// cfg.Source.ExcludeFile is read here, once, and its patterns appended to
// cfg.Source.Excludes — so the source filters on the full list and the exclude
// hash covers the same list. That hash is what the engine compares against the
// previous snapshot's to decide between an incremental and a full rescan, which
// is why deriving it is this package's job and not a caller's.
func Backup(ctx context.Context, cfg config.Backup, opts ...Option) (*BackupJob, error) {
	o := newOptions(opts)

	patterns, err := excludePatterns(cfg.Source)
	if err != nil {
		return nil, err
	}
	cfg.Source.Excludes = patterns

	src, err := openSource(ctx, cfg.Source, o)
	if err != nil {
		return nil, err
	}
	return &BackupJob{Source: src, Options: backupOptions(cfg)}, nil
}

// BackupOptions returns the client options describing how to record a backup,
// including the exclude hash derived from cfg's patterns.
//
// Prefer Backup, which constructs the source from the same resolved patterns.
// This exists for a caller supplying its own source.Source implementation, which
// must then apply cfg.Source.Excludes itself for the hash to mean anything.
func BackupOptions(cfg config.Backup) ([]cloudstic.BackupOption, error) {
	patterns, err := excludePatterns(cfg.Source)
	if err != nil {
		return nil, err
	}
	cfg.Source.Excludes = patterns
	return backupOptions(cfg), nil
}

func backupOptions(cfg config.Backup) []cloudstic.BackupOption {
	var opts []cloudstic.BackupOption
	if cfg.DryRun {
		opts = append(opts, cloudstic.WithBackupDryRun())
	}
	if cfg.IgnoreEmpty {
		opts = append(opts, cloudstic.WithIgnoreEmptySnapshot())
	}
	if len(cfg.Tags) > 0 {
		opts = append(opts, cloudstic.WithTags(cfg.Tags...))
	}
	if len(cfg.Source.Excludes) > 0 {
		opts = append(opts, cloudstic.WithExcludeHash(source.ExcludeHash(cfg.Source.Excludes)))
	}
	return opts
}

// excludePatterns returns cfg's patterns with any exclude file's appended.
func excludePatterns(cfg config.Source) ([]string, error) {
	if cfg.ExcludeFile == "" {
		return cfg.Excludes, nil
	}
	fromFile, err := source.ParseExcludeFile(cfg.ExcludeFile)
	if err != nil {
		return nil, err
	}
	return append(append([]string(nil), cfg.Excludes...), fromFile...), nil
}
