package main

import "github.com/cloudstic/cli/pkg/config"

// URI parsing lives in pkg/config: it is resolution, not construction, and it
// is the half of the store contract a caller can use without linking a cloud
// SDK. Seventeen call sites in this package parse a URI purely to validate one
// — profile and store commands, the TUI forms, the config tables — and none of
// them should have to pay for the AWS or Google client to do it (RFC 0022 §7).
//
// The types stay spellable under their original names here so this package
// reads as it did.
type (
	storeURIParts  = config.StoreURI
	sourceURIParts = config.SourceURI
)
