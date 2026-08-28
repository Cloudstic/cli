package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
)

// promptStoreSelection asks the user to pick an existing store or describe a new
// one, adding the new entry to cfg in place. It reports the chosen reference name
// and whether it was just created.
//
// A free function taking the runner rather than a method on it: this is a
// profiles-domain workflow that happens to prompt, not a capability of the
// runner, which holds only I/O primitives. It returns an error rather than an
// exit code for the same reason — deciding a process exit status is the
// command's job.
//
// Its errors start with a verb phrase ("select store: …") so a caller reporting
// them as `r.fail("Failed to %v", err)` reproduces the message this produced
// when it formatted its own failures, which keeps the refactor invisible to
// users.
func promptStoreSelection(r *runner, ctx context.Context, cfg *profile.Config) (name string, created bool, err error) {
	options := []string{"Create new store"}
	for name := range cfg.Stores {
		options = append(options, name)
	}
	sort.Strings(options[1:])

	picked, err := r.promptSelect(ctx, "Select a store", options)
	if err != nil {
		return "", false, fmt.Errorf("select store: %w", err)
	}

	if picked != "Create new store" {
		return picked, false, nil
	}

	// Create a new store inline.
	refName, err := r.promptValidatedLine(ctx, "Store reference name", "default-store", func(v string) error {
		if v == "" {
			return fmt.Errorf("store reference name is required")
		}
		return validateRefName("store", v)
	})
	if err != nil {
		return "", false, fmt.Errorf("read store reference name: %w", err)
	}
	uri, err := r.promptValidatedLine(ctx, "Store URI (e.g. s3:bucket/path, local:/path, sftp://host/path)", "", func(v string) error {
		if v == "" {
			return fmt.Errorf("store URI is required")
		}
		_, err := config.ParseStoreURI(v)
		if err != nil {
			return fmt.Errorf("invalid store URI: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("read store URI: %w", err)
	}
	cfg.Stores[refName] = profile.Store{URI: uri}
	return refName, true, nil
}

// promptAuthSelection prompts the user to pick an existing auth entry (filtered
// by provider) or create a new one, adding the new entry to cfg.Auth in place.
// It reports the chosen auth-ref name.
//
// See promptStoreSelection for why this is a free function returning an error
// rather than a runner method returning an exit code.
func promptAuthSelection(r *runner, ctx context.Context, cfg *profile.Config, provider, profileName string) (string, error) {
	options := []string{"Create new auth"}
	for name, auth := range cfg.Auth {
		if auth.Provider == provider {
			options = append(options, name)
		}
	}
	sort.Strings(options[1:])

	picked, err := r.promptSelect(ctx, fmt.Sprintf("Select %s auth entry", provider), options)
	if err != nil {
		return "", fmt.Errorf("select auth entry: %w", err)
	}
	if picked != "Create new auth" {
		return picked, nil
	}

	refName, err := r.promptValidatedLine(ctx, "Auth reference name", provider+"-"+profileName, func(v string) error {
		if v == "" {
			return fmt.Errorf("auth reference name is required")
		}
		return validateRefName("auth", v)
	})
	if err != nil {
		return "", fmt.Errorf("read auth reference name: %w", err)
	}

	defTokenRef := config.DefaultAuthTokenRef(provider, refName)
	tokenStorage, err := r.promptLine(ctx, "Token storage (file path or secret ref)", defTokenRef)
	if err != nil {
		return "", fmt.Errorf("read token storage: %w", err)
	}
	if tokenStorage == "" {
		tokenStorage = defTokenRef
	}

	auth := profile.Auth{Provider: provider}
	if strings.Contains(tokenStorage, "://") {
		switch provider {
		case "google":
			auth.GoogleTokenRef = tokenStorage
		case "onedrive":
			auth.OneDriveTokenRef = tokenStorage
		}
	} else {
		switch provider {
		case "google":
			auth.GoogleTokenFile = tokenStorage
		case "onedrive":
			auth.OneDriveTokenFile = tokenStorage
		}
	}
	cfg.Auth[refName] = auth
	return refName, nil
}

func prefillProfileArgs(a *profileNewArgs, p profile.Profile) {
	if !a.flagProvided("source") && p.Source != "" {
		a.source = p.Source
	}
	if !a.flagProvided("store-ref") && p.Store != "" {
		a.storeRef = p.Store
	}
	if !a.flagProvided("auth-ref") && p.AuthRef != "" {
		a.authRef = p.AuthRef
	}
	if !a.flagProvided("exclude-file") && p.ExcludeFile != "" {
		a.excludeFile = p.ExcludeFile
	}
	if !a.flagProvided("skip-native-files") && p.SkipNativeFiles {
		a.skipNativeFiles = true
	}
	if !a.flagProvided("volume-uuid") && p.VolumeUUID != "" {
		a.volumeUUID = p.VolumeUUID
	}
	if !a.flagProvided("google-credentials") && p.GoogleCreds != "" {
		a.googleCreds = p.GoogleCreds
	}
	if !a.flagProvided("google-credentials-ref") && p.GoogleCredsRef != "" {
		a.googleCredsRef = p.GoogleCredsRef
	}
	if !a.flagProvided("google-credentials-json") && p.GoogleCredsJSON != "" {
		a.googleCredsJSON = p.GoogleCredsJSON
	}
	if !a.flagProvided("google-token-file") && p.GoogleTokenFile != "" {
		a.googleTokenFile = p.GoogleTokenFile
	}
	if !a.flagProvided("google-token-ref") && p.GoogleTokenRef != "" {
		a.googleTokenRef = p.GoogleTokenRef
	}
	if !a.flagProvided("onedrive-client-id") && p.OneDriveClientID != "" {
		a.onedriveClientID = p.OneDriveClientID
	}
	if !a.flagProvided("onedrive-token-file") && p.OneDriveTokenFile != "" {
		a.onedriveTokenFile = p.OneDriveTokenFile
	}
	if !a.flagProvided("onedrive-token-ref") && p.OneDriveTokenRef != "" {
		a.onedriveTokenRef = p.OneDriveTokenRef
	}
	if len(a.tags) == 0 && len(p.Tags) > 0 {
		a.tags = append(stringArrayFlags{}, p.Tags...)
	}
	if len(a.excludes) == 0 && len(p.Excludes) > 0 {
		a.excludes = append(stringArrayFlags{}, p.Excludes...)
	}
}

func profileProviderFromSource(sourceURI string) string {
	uri, err := config.ParseSourceURI(sourceURI)
	if err != nil {
		return ""
	}
	switch uri.Scheme {
	case "gdrive", "gdrive-changes":
		return "google"
	case "onedrive", "onedrive-changes":
		return "onedrive"
	default:
		return ""
	}
}
