package config

import (
	"testing"
)

func TestParseStoreURI(t *testing.T) {
	tests := []struct {
		raw     string
		want    StoreURI
		wantErr bool
	}{
		// local
		{raw: "local:./backup_store", want: StoreURI{Scheme: "local", Path: "./backup_store"}},
		{raw: "local:/abs/path", want: StoreURI{Scheme: "local", Path: "/abs/path"}},
		{raw: "local:", wantErr: true},

		// s3
		{raw: "s3:my-bucket", want: StoreURI{Scheme: "s3", Bucket: "my-bucket"}},
		{raw: "s3:my-bucket/prod", want: StoreURI{Scheme: "s3", Bucket: "my-bucket", Prefix: "prod"}},
		{raw: "s3:my-bucket/nested/prefix", want: StoreURI{Scheme: "s3", Bucket: "my-bucket", Prefix: "nested/prefix"}},
		{raw: "s3:", wantErr: true},

		// b2
		{raw: "b2:my-bucket", want: StoreURI{Scheme: "b2", Bucket: "my-bucket"}},
		{raw: "b2:my-bucket/prod", want: StoreURI{Scheme: "b2", Bucket: "my-bucket", Prefix: "prod"}},
		{raw: "b2:", wantErr: true},

		// sftp
		{raw: "sftp://host.example.com/backups", want: StoreURI{Scheme: "sftp", Host: "host.example.com", Path: "/backups"}},
		{raw: "sftp://user@host.example.com/backups", want: StoreURI{Scheme: "sftp", Host: "host.example.com", User: "user", Path: "/backups"}},
		{raw: "sftp://user@host.example.com:2222/backups", want: StoreURI{Scheme: "sftp", Host: "host.example.com", Port: "2222", User: "user", Path: "/backups"}},
		{raw: "sftp://host.example.com:22/backups", want: StoreURI{Scheme: "sftp", Host: "host.example.com", Port: "22", Path: "/backups"}},
		{raw: "sftp:///no-host", wantErr: true},

		// invalid
		{raw: "no-colon", wantErr: true},
		{raw: "unknown:value", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseStoreURI(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseStoreURI(%q): expected error, got %+v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseStoreURI(%q): unexpected error: %v", tc.raw, err)
			}
			if *got != tc.want {
				t.Errorf("ParseStoreURI(%q):\n  got  %+v\n  want %+v", tc.raw, *got, tc.want)
			}
		})
	}
}

func TestParseSourceURI(t *testing.T) {
	tests := []struct {
		raw     string
		want    SourceURI
		wantErr bool
	}{
		// local
		{raw: "local:./documents", want: SourceURI{Scheme: "local", Path: "./documents"}},
		{raw: "local:/abs/path", want: SourceURI{Scheme: "local", Path: "/abs/path"}},
		{raw: "local:", wantErr: true},

		// sftp
		{raw: "sftp://host.example.com/data", want: SourceURI{Scheme: "sftp", Host: "host.example.com", Path: "/data"}},
		{raw: "sftp://user@host.example.com/data", want: SourceURI{Scheme: "sftp", Host: "host.example.com", User: "user", Path: "/data"}},
		{raw: "sftp://user@host.example.com:2222/data", want: SourceURI{Scheme: "sftp", Host: "host.example.com", Port: "2222", User: "user", Path: "/data"}},
		{raw: "sftp:///no-host", wantErr: true},

		// cloud keywords
		{raw: "gdrive", want: SourceURI{Scheme: "gdrive", Path: "/"}},
		{raw: "gdrive-changes", want: SourceURI{Scheme: "gdrive-changes", Path: "/"}},
		{raw: "onedrive", want: SourceURI{Scheme: "onedrive", Path: "/"}},
		{raw: "onedrive-changes", want: SourceURI{Scheme: "onedrive-changes", Path: "/"}},
		{raw: "gdrive:/some/path", want: SourceURI{Scheme: "gdrive", Path: "/some/path"}},
		{raw: "gdrive:some/path", want: SourceURI{Scheme: "gdrive", Path: "/some/path"}},
		{raw: "onedrive:/documents", want: SourceURI{Scheme: "onedrive", Path: "/documents"}},
		{raw: "gdrive://My Shared Drive/some/path", want: SourceURI{Scheme: "gdrive", Host: "My Shared Drive", Path: "/some/path"}},
		{raw: "gdrive-changes://Company Data/finance", want: SourceURI{Scheme: "gdrive-changes", Host: "Company Data", Path: "/finance"}},
		{raw: "onedrive://Personal/documents", want: SourceURI{Scheme: "onedrive", Host: "Personal", Path: "/documents"}},
		{raw: "onedrive-changes://Shared/photos", want: SourceURI{Scheme: "onedrive-changes", Host: "Shared", Path: "/photos"}},

		// invalid
		{raw: "sftp", wantErr: true},
		{raw: "local", wantErr: true},
		{raw: "unknown:value", wantErr: true},
		{raw: "unknown-keyword", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseSourceURI(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSourceURI(%q): expected error, got %+v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSourceURI(%q): unexpected error: %v", tc.raw, err)
			}
			if *got != tc.want {
				t.Errorf("ParseSourceURI(%q):\n  got  %+v\n  want %+v", tc.raw, *got, tc.want)
			}
		})
	}
}
