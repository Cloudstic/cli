package main

import "github.com/cloudstic/cli/pkg/config"

// URI parsing lives in pkg/config, and store construction in pkg/open, so the
// store-side parsed type is no longer named in this package at all.
//
// The source-side one still is: building a source from a URI has not moved
// yet, so cmd_backup.go's sftpSourceOpts continues to take one (RFC 0022 §7).
type sourceURIParts = config.SourceURI
