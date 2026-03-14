package main

import (
	"flag"
	"testing"

	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

func newTestLocalStore(t *testing.T) *store.LocalStore {
	t.Helper()
	s, err := store.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	return s
}

func TestParseStoreURI(t *testing.T) {
	tests := []struct {
		raw     string
		want    storeURIParts
		wantErr bool
	}{
		// local
		{raw: "local:./backup_store", want: storeURIParts{scheme: "local", path: "./backup_store"}},
		{raw: "local:/abs/path", want: storeURIParts{scheme: "local", path: "/abs/path"}},
		{raw: "local:", wantErr: true},

		// s3
		{raw: "s3:my-bucket", want: storeURIParts{scheme: "s3", bucket: "my-bucket"}},
		{raw: "s3:my-bucket/prod", want: storeURIParts{scheme: "s3", bucket: "my-bucket", prefix: "prod"}},
		{raw: "s3:my-bucket/nested/prefix", want: storeURIParts{scheme: "s3", bucket: "my-bucket", prefix: "nested/prefix"}},
		{raw: "s3:", wantErr: true},

		// b2
		{raw: "b2:my-bucket", want: storeURIParts{scheme: "b2", bucket: "my-bucket"}},
		{raw: "b2:my-bucket/prod", want: storeURIParts{scheme: "b2", bucket: "my-bucket", prefix: "prod"}},
		{raw: "b2:", wantErr: true},

		// sftp
		{raw: "sftp://host.example.com/backups", want: storeURIParts{scheme: "sftp", host: "host.example.com", path: "/backups"}},
		{raw: "sftp://user@host.example.com/backups", want: storeURIParts{scheme: "sftp", host: "host.example.com", user: "user", path: "/backups"}},
		{raw: "sftp://user@host.example.com:2222/backups", want: storeURIParts{scheme: "sftp", host: "host.example.com", port: "2222", user: "user", path: "/backups"}},
		{raw: "sftp://host.example.com:22/backups", want: storeURIParts{scheme: "sftp", host: "host.example.com", port: "22", path: "/backups"}},
		{raw: "sftp:///no-host", wantErr: true},

		// invalid
		{raw: "no-colon", wantErr: true},
		{raw: "unknown:value", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseStoreURI(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseStoreURI(%q): expected error, got %+v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStoreURI(%q): unexpected error: %v", tc.raw, err)
			}
			if *got != tc.want {
				t.Errorf("parseStoreURI(%q):\n  got  %+v\n  want %+v", tc.raw, *got, tc.want)
			}
		})
	}
}

func TestParseSourceURI(t *testing.T) {
	tests := []struct {
		raw     string
		want    sourceURIParts
		wantErr bool
	}{
		// local
		{raw: "local:./documents", want: sourceURIParts{scheme: "local", path: "./documents"}},
		{raw: "local:/abs/path", want: sourceURIParts{scheme: "local", path: "/abs/path"}},
		{raw: "local:", wantErr: true},

		// sftp
		{raw: "sftp://host.example.com/data", want: sourceURIParts{scheme: "sftp", host: "host.example.com", path: "/data"}},
		{raw: "sftp://user@host.example.com/data", want: sourceURIParts{scheme: "sftp", host: "host.example.com", user: "user", path: "/data"}},
		{raw: "sftp://user@host.example.com:2222/data", want: sourceURIParts{scheme: "sftp", host: "host.example.com", port: "2222", user: "user", path: "/data"}},
		{raw: "sftp:///no-host", wantErr: true},

		// cloud keywords
		{raw: "gdrive", want: sourceURIParts{scheme: "gdrive", path: "/"}},
		{raw: "gdrive-changes", want: sourceURIParts{scheme: "gdrive-changes", path: "/"}},
		{raw: "onedrive", want: sourceURIParts{scheme: "onedrive", path: "/"}},
		{raw: "onedrive-changes", want: sourceURIParts{scheme: "onedrive-changes", path: "/"}},
		{raw: "gdrive:/some/path", want: sourceURIParts{scheme: "gdrive", path: "/some/path"}},
		{raw: "gdrive:some/path", want: sourceURIParts{scheme: "gdrive", path: "/some/path"}},
		{raw: "onedrive:/documents", want: sourceURIParts{scheme: "onedrive", path: "/documents"}},

		// invalid
		{raw: "sftp", wantErr: true},
		{raw: "local", wantErr: true},
		{raw: "unknown:value", wantErr: true},
		{raw: "unknown-keyword", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseSourceURI(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSourceURI(%q): expected error, got %+v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSourceURI(%q): unexpected error: %v", tc.raw, err)
			}
			if *got != tc.want {
				t.Errorf("parseSourceURI(%q):\n  got  %+v\n  want %+v", tc.raw, *got, tc.want)
			}
		})
	}
}

func TestApplyDebug_Disabled(t *testing.T) {
	logger.Writer = nil

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	_ = fs.Parse([]string{}) // --debug defaults to false

	inner := newTestLocalStore(t)
	result := g.applyDebug(inner)

	// Without --debug, the store should be returned as-is.
	if result != inner {
		t.Error("Expected applyDebug to return the original store when --debug is false")
	}
	if logger.Writer != nil {
		t.Error("Expected logger.Writer to remain nil when --debug is false")
	}
}

func TestApplyDebug_Enabled(t *testing.T) {
	logger.Writer = nil
	defer func() { logger.Writer = nil }()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	_ = fs.Parse([]string{"-debug"})

	inner := newTestLocalStore(t)
	result := g.applyDebug(inner)

	// With --debug, the store should be wrapped in a DebugStore.
	if result == inner {
		t.Error("Expected applyDebug to wrap the store when --debug is true")
	}
	if _, ok := result.(*store.DebugStore); !ok {
		t.Errorf("Expected result to be *store.DebugStore, got %T", result)
	}
	if logger.Writer == nil {
		t.Error("Expected logger.Writer to be set when --debug is true")
	}
	if g.debugLog == nil {
		t.Error("Expected globalFlags.debugLog to be initialized when --debug is true")
	}
}

func TestApplyDebug_NilDebugField(t *testing.T) {
	logger.Writer = nil

	g := &globalFlags{} // debug field is nil
	inner := newTestLocalStore(t)
	result := g.applyDebug(inner)

	// With nil debug pointer, the store should be returned as-is.
	if result != inner {
		t.Error("Expected applyDebug to return the original store when debug is nil")
	}
}
