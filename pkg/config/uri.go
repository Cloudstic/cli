package config

import (
	"fmt"
	"net/url"
	"strings"
)

// StoreURI holds the parsed components of a store URI.
//
// Which fields are populated depends on Scheme: local uses Path, s3 and b2 use
// Bucket and Prefix, sftp uses Host, Port, User and Path.
type StoreURI struct {
	Scheme string // "local", "s3", "b2", "sftp"
	// S3/B2 fields
	Bucket string
	Prefix string
	// local field
	Path string
	// SFTP fields
	Host string
	Port string
	User string
}

// ParseStoreURI parses a store URI into its components.
//
// Supported formats:
//
//	local:<path>                        e.g. local:./backup_store
//	s3:<bucket>[/<prefix>]              e.g. s3:my-bucket or s3:my-bucket/prod
//	b2:<bucket>[/<prefix>]              e.g. b2:my-bucket or b2:my-bucket/prod
//	sftp://[user@]host[:port]/<path>    e.g. sftp://backup@host.com/backups
func ParseStoreURI(raw string) (*StoreURI, error) {
	if strings.HasPrefix(raw, "sftp://") {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid store URI %q: %w", raw, err)
		}
		if u.Hostname() == "" {
			return nil, fmt.Errorf("invalid store URI %q: sftp URI must include a hostname", raw)
		}
		user := ""
		if u.User != nil {
			user = u.User.Username()
		}
		return &StoreURI{
			Scheme: "sftp",
			Host:   u.Hostname(),
			Port:   u.Port(),
			User:   user,
			Path:   u.Path,
		}, nil
	}

	idx := strings.IndexByte(raw, ':')
	if idx < 0 {
		return nil, fmt.Errorf("invalid store URI %q: missing scheme (e.g. local:./path, s3:bucket, b2:bucket, sftp://host/path)", raw)
	}
	scheme := raw[:idx]
	rest := raw[idx+1:]

	switch scheme {
	case "local":
		if rest == "" {
			return nil, fmt.Errorf("invalid store URI %q: local path cannot be empty", raw)
		}
		return &StoreURI{Scheme: "local", Path: rest}, nil
	case "s3", "b2":
		if rest == "" {
			return nil, fmt.Errorf("invalid store URI %q: bucket name cannot be empty", raw)
		}
		bucket, prefix, _ := strings.Cut(rest, "/")
		if bucket == "" {
			return nil, fmt.Errorf("invalid store URI %q: bucket name cannot be empty", raw)
		}
		return &StoreURI{Scheme: scheme, Bucket: bucket, Prefix: prefix}, nil
	default:
		return nil, fmt.Errorf("unknown store scheme %q in %q: supported schemes are local, s3, b2, sftp", scheme, raw)
	}
}

// SourceURI holds the parsed components of a source URI or keyword.
//
// Which fields are populated depends on Scheme: local uses Path, sftp uses
// Host, Port, User and Path. For the cloud schemes (gdrive, onedrive, and
// their -changes variants) Host carries the *drive name* rather than a
// network host, and Path the sub-path within that drive.
type SourceURI struct {
	Scheme string // "local", "sftp", "gdrive", "gdrive-changes", "onedrive", "onedrive-changes"
	// local/sftp fields
	Path string
	// sftp-specific fields
	Host string
	Port string
	User string
}

// ParseSourceURI parses a source URI or keyword into its components.
//
// Supported formats:
//
//	local:<path>                        e.g. local:./documents
//	sftp://[user@]host[:port]/<path>    e.g. sftp://backup@host.com/data
//	gdrive
//	gdrive-changes
//	onedrive
//	onedrive-changes
func ParseSourceURI(raw string) (*SourceURI, error) {
	if strings.HasPrefix(raw, "sftp://") {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid source URI %q: %w", raw, err)
		}
		if u.Hostname() == "" {
			return nil, fmt.Errorf("invalid source URI %q: sftp URI must include a hostname", raw)
		}
		user := ""
		if u.User != nil {
			user = u.User.Username()
		}
		return &SourceURI{
			Scheme: "sftp",
			Host:   u.Hostname(),
			Port:   u.Port(),
			User:   user,
			Path:   u.Path,
		}, nil
	}

	idx := strings.IndexByte(raw, ':')
	if idx >= 0 {
		scheme := raw[:idx]
		rest := raw[idx+1:]
		switch scheme {
		case "local":
			if rest == "" {
				return nil, fmt.Errorf("invalid source URI %q: local path cannot be empty", raw)
			}
			return &SourceURI{Scheme: "local", Path: rest}, nil
		case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
			if strings.HasPrefix(rest, "//") {
				// Format: scheme://Drive Name/path
				rest = rest[2:]
				idx := strings.IndexByte(rest, '/')
				driveName := ""
				path := "/"
				if idx >= 0 {
					driveName = rest[:idx]
					path = ensureLeadingSlash(rest[idx:])
				} else {
					driveName = rest
				}
				return &SourceURI{Scheme: scheme, Host: driveName, Path: path}, nil
			}
			return &SourceURI{Scheme: scheme, Path: ensureLeadingSlash(rest)}, nil
		default:
			return nil, fmt.Errorf("unknown source scheme %q in %q: supported URI formats are local:<path> and sftp://[user@]host[:port]/<path>", scheme, raw)
		}
	}

	// Bare keyword (cloud sources)
	switch raw {
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		return &SourceURI{Scheme: raw, Path: "/"}, nil
	default:
		return nil, fmt.Errorf("unknown source %q: supported values are local:<path>, sftp://[user@]host[:port]/<path>, gdrive[:<path>], gdrive-changes[:<path>], onedrive[:<path>], onedrive-changes[:<path>]", raw)
	}
}

func ensureLeadingSlash(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") {
		return "/" + s
	}
	return s
}
