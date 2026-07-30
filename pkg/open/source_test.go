package open

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/source"
)

// These moved here with initSource, which used to live in package main and so
// could only be exercised through the CLI's flag structs (RFC 0022 §7).

func TestSource_Local(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name string
		cfg  config.Source
		want func(*testing.T, source.SourceInfo)
	}{
		{
			name: "plain",
			cfg:  config.Source{URI: "local:" + dir},
			want: func(t *testing.T, info source.SourceInfo) {
				if info.Type != "local" {
					t.Errorf("type = %q, want local", info.Type)
				}
			},
		},
		{
			name: "metadata switches",
			cfg: config.Source{
				URI: "local:" + dir, SkipMode: true, SkipFlags: true, SkipXattrs: true,
				XattrNamespaces: []string{"user.", "com.apple."},
			},
			want: func(t *testing.T, info source.SourceInfo) {
				if info.Type != "local" {
					t.Errorf("type = %q, want local", info.Type)
				}
			},
		},
		{
			// The override is what lets a portable drive back up incrementally
			// from more than one machine, so it has to reach the source identity.
			name: "volume uuid overrides identity",
			cfg:  config.Source{URI: "local:" + dir, VolumeUUID: "test-uuid-123"},
			want: func(t *testing.T, info source.SourceInfo) {
				if info.Identity != "test-uuid-123" {
					t.Errorf("identity = %q, want the configured volume UUID", info.Identity)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := Source(context.Background(), tc.cfg)
			if err != nil {
				t.Fatalf("Source: %v", err)
			}
			if src == nil {
				t.Fatal("Source returned nil without an error")
			}
			tc.want(t, src.Info())
		})
	}
}

func TestSource_UnknownScheme(t *testing.T) {
	_, err := Source(context.Background(), config.Source{URI: "invalid-source:/"})
	if err == nil {
		t.Fatal("expected an error for an unknown source scheme")
	}
	if !strings.Contains(err.Error(), "unknown source scheme") {
		t.Errorf("error %q does not name the scheme problem", err)
	}
}

// sftpSourceOpts takes a resolved config.SFTP rather than reaching for flags, so
// it is exercised without dialing anything.
func TestSFTPSourceOpts_TranslatesConfig(t *testing.T) {
	uri := &config.SourceURI{Host: "host.example.com", Port: "2222", User: "backup", Path: "/data"}

	if got := len(sftpSourceOpts(config.SFTP{}, uri)); got != 3 {
		t.Errorf("minimal config: %d options, want 3 (base path, port, user)", got)
	}
	full := sftpSourceOpts(config.SFTP{
		Password:   "s3cret",
		Key:        "/path/to/key",
		Insecure:   true,
		KnownHosts: "/path/to/known_hosts",
	}, uri)
	if len(full) != 7 {
		t.Errorf("full config: %d options, want 7", len(full))
	}
}

func TestTokenPath(t *testing.T) {
	dir := t.TempDir()

	// An explicit path is used as given.
	got, err := tokenPath(dir, "/explicit/token.json", "google_token.json")
	if err != nil {
		t.Fatalf("tokenPath: %v", err)
	}
	if got != "/explicit/token.json" {
		t.Errorf("tokenPath = %q, want the explicit path", got)
	}

	// Otherwise the default name lands in the config directory.
	got, err = tokenPath(dir, "", "google_token.json")
	if err != nil {
		t.Fatalf("tokenPath: %v", err)
	}
	if want := filepath.Join(dir, "google_token.json"); got != want {
		t.Errorf("tokenPath = %q, want %q", got, want)
	}
}

func TestBackupOptions(t *testing.T) {
	t.Run("ignore empty snapshot", func(t *testing.T) {
		opts, err := BackupOptions(config.Backup{IgnoreEmpty: true})
		if err != nil {
			t.Fatalf("BackupOptions: %v", err)
		}
		if len(opts) != 1 {
			t.Errorf("got %d options, want 1", len(opts))
		}
	})

	t.Run("no excludes means no exclude hash", func(t *testing.T) {
		opts, err := BackupOptions(config.Backup{})
		if err != nil {
			t.Fatalf("BackupOptions: %v", err)
		}
		if len(opts) != 0 {
			t.Errorf("got %d options for a zero config, want none", len(opts))
		}
	})

	t.Run("a missing exclude file is an error", func(t *testing.T) {
		_, err := BackupOptions(config.Backup{
			Source: config.Source{ExcludeFile: filepath.Join(t.TempDir(), "absent")},
		})
		if err == nil {
			t.Fatal("expected an error for an unreadable exclude file")
		}
	})
}

// TestBackup_ExcludeHashCoversTheExcludeFile is the reason Backup returns the
// source and the options together.
//
// The exclude hash recorded on a snapshot must be the hash of exactly the
// patterns the source filters on. The patterns come from two places — the config
// and a file — so a hash derived without reading the file describes a different
// exclude set than the one in force, and the engine reads that difference as
// "the patterns changed" on every subsequent run.
func TestBackup_ExcludeHashCoversTheExcludeFile(t *testing.T) {
	dir := t.TempDir()
	excludeFile := filepath.Join(dir, ".backupignore")
	if err := os.WriteFile(excludeFile, []byte("*.tmp\nbuild/\n"), 0o600); err != nil {
		t.Fatalf("write exclude file: %v", err)
	}

	cfg := config.Backup{Source: config.Source{
		URI:         "local:" + dir,
		Excludes:    []string{"*.log"},
		ExcludeFile: excludeFile,
	}}

	job, err := Backup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if job.Source == nil {
		t.Fatal("Backup returned no source")
	}

	// The hash must match the combined list, not just the inline patterns.
	want := source.ExcludeHash([]string{"*.log", "*.tmp", "build/"})
	if got := source.ExcludeHash([]string{"*.log"}); got == want {
		t.Fatal("test is vacuous: the inline-only hash equals the combined one")
	}
	if len(job.Options) == 0 {
		t.Fatal("Backup produced no options, so it recorded no exclude hash")
	}

	// Compare through BackupOptions, which resolves the same way.
	opts, err := BackupOptions(cfg)
	if err != nil {
		t.Fatalf("BackupOptions: %v", err)
	}
	if len(opts) != len(job.Options) {
		t.Errorf("Backup and BackupOptions disagree: %d vs %d options", len(job.Options), len(opts))
	}
}
