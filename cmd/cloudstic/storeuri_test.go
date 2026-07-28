package main

import (
	"testing"
)

func TestParseStoreURI(t *testing.T) {
	tests := []struct {
		raw     string
		want    storeURIParts
		wantErr bool
	}{
		// local
		{raw: "local:./backup_store", want: storeURIParts{Scheme: "local", Path: "./backup_store"}},
		{raw: "local:/abs/path", want: storeURIParts{Scheme: "local", Path: "/abs/path"}},
		{raw: "local:", wantErr: true},

		// s3
		{raw: "s3:my-bucket", want: storeURIParts{Scheme: "s3", Bucket: "my-bucket"}},
		{raw: "s3:my-bucket/prod", want: storeURIParts{Scheme: "s3", Bucket: "my-bucket", Prefix: "prod"}},
		{raw: "s3:my-bucket/nested/prefix", want: storeURIParts{Scheme: "s3", Bucket: "my-bucket", Prefix: "nested/prefix"}},
		{raw: "s3:", wantErr: true},

		// b2
		{raw: "b2:my-bucket", want: storeURIParts{Scheme: "b2", Bucket: "my-bucket"}},
		{raw: "b2:my-bucket/prod", want: storeURIParts{Scheme: "b2", Bucket: "my-bucket", Prefix: "prod"}},
		{raw: "b2:", wantErr: true},

		// sftp
		{raw: "sftp://host.example.com/backups", want: storeURIParts{Scheme: "sftp", Host: "host.example.com", Path: "/backups"}},
		{raw: "sftp://user@host.example.com/backups", want: storeURIParts{Scheme: "sftp", Host: "host.example.com", User: "user", Path: "/backups"}},
		{raw: "sftp://user@host.example.com:2222/backups", want: storeURIParts{Scheme: "sftp", Host: "host.example.com", Port: "2222", User: "user", Path: "/backups"}},
		{raw: "sftp://host.example.com:22/backups", want: storeURIParts{Scheme: "sftp", Host: "host.example.com", Port: "22", Path: "/backups"}},
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
		{raw: "local:./documents", want: sourceURIParts{Scheme: "local", Path: "./documents"}},
		{raw: "local:/abs/path", want: sourceURIParts{Scheme: "local", Path: "/abs/path"}},
		{raw: "local:", wantErr: true},

		// sftp
		{raw: "sftp://host.example.com/data", want: sourceURIParts{Scheme: "sftp", Host: "host.example.com", Path: "/data"}},
		{raw: "sftp://user@host.example.com/data", want: sourceURIParts{Scheme: "sftp", Host: "host.example.com", User: "user", Path: "/data"}},
		{raw: "sftp://user@host.example.com:2222/data", want: sourceURIParts{Scheme: "sftp", Host: "host.example.com", Port: "2222", User: "user", Path: "/data"}},
		{raw: "sftp:///no-host", wantErr: true},

		// cloud keywords
		{raw: "gdrive", want: sourceURIParts{Scheme: "gdrive", Path: "/"}},
		{raw: "gdrive-changes", want: sourceURIParts{Scheme: "gdrive-changes", Path: "/"}},
		{raw: "onedrive", want: sourceURIParts{Scheme: "onedrive", Path: "/"}},
		{raw: "onedrive-changes", want: sourceURIParts{Scheme: "onedrive-changes", Path: "/"}},
		{raw: "gdrive:/some/path", want: sourceURIParts{Scheme: "gdrive", Path: "/some/path"}},
		{raw: "gdrive:some/path", want: sourceURIParts{Scheme: "gdrive", Path: "/some/path"}},
		{raw: "onedrive:/documents", want: sourceURIParts{Scheme: "onedrive", Path: "/documents"}},
		{raw: "gdrive://My Shared Drive/some/path", want: sourceURIParts{Scheme: "gdrive", Host: "My Shared Drive", Path: "/some/path"}},
		{raw: "gdrive-changes://Company Data/finance", want: sourceURIParts{Scheme: "gdrive-changes", Host: "Company Data", Path: "/finance"}},
		{raw: "onedrive://Personal/documents", want: sourceURIParts{Scheme: "onedrive", Host: "Personal", Path: "/documents"}},
		{raw: "onedrive-changes://Shared/photos", want: sourceURIParts{Scheme: "onedrive-changes", Host: "Shared", Path: "/photos"}},

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
