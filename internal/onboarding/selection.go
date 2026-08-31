package onboarding

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
)

// SelectStore asks the user to pick an existing store or describe a new
// one, adding the new entry to cfg in place. It reports the chosen reference name
// and whether it was just created.
//
// It takes a Prompter rather than the CLI runner: this is a profiles-domain
// workflow that happens to prompt. It returns an error rather than an exit
// code for the same reason — deciding a process exit status is the command's
// job.
//
// Its errors start with a verb phrase ("select store: …") so a caller reporting
// them as `r.fail("Failed to %v", err)` reproduces the message this produced
// when it formatted its own failures, which keeps the refactor invisible to
// users.
func SelectStore(ctx context.Context, p Prompter, cfg *profile.Config) (name string, created bool, err error) {
	options := []string{"Create new store"}
	for name := range cfg.Stores {
		options = append(options, name)
	}
	sort.Strings(options[1:])

	picked, err := p.PromptSelect(ctx, "Select a store", options)
	if err != nil {
		return "", false, fmt.Errorf("select store: %w", err)
	}

	if picked != "Create new store" {
		return picked, false, nil
	}

	// Create a new store inline.
	refName, err := p.PromptValidatedLine(ctx, "Store reference name", "default-store", func(v string) error {
		if v == "" {
			return fmt.Errorf("store reference name is required")
		}
		return ValidateRefName("store", v)
	})
	if err != nil {
		return "", false, fmt.Errorf("read store reference name: %w", err)
	}
	uri, err := p.PromptValidatedLine(ctx, "Store URI (e.g. s3:bucket/path, local:/path, sftp://host/path)", "", func(v string) error {
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

// SelectAuth prompts the user to pick an existing auth entry (filtered
// by provider) or create a new one, adding the new entry to cfg.Auth in place.
// It reports the chosen auth-ref name.
//
// See SelectStore for why this is a free function returning an error
// rather than a runner method returning an exit code.
func SelectAuth(ctx context.Context, p Prompter, cfg *profile.Config, provider, profileName string) (string, error) {
	options := []string{"Create new auth"}
	for name, auth := range cfg.Auth {
		if auth.Provider == provider {
			options = append(options, name)
		}
	}
	sort.Strings(options[1:])

	picked, err := p.PromptSelect(ctx, fmt.Sprintf("Select %s auth entry", provider), options)
	if err != nil {
		return "", fmt.Errorf("select auth entry: %w", err)
	}
	if picked != "Create new auth" {
		return picked, nil
	}

	refName, err := p.PromptValidatedLine(ctx, "Auth reference name", provider+"-"+profileName, func(v string) error {
		if v == "" {
			return fmt.Errorf("auth reference name is required")
		}
		return ValidateRefName("auth", v)
	})
	if err != nil {
		return "", fmt.Errorf("read auth reference name: %w", err)
	}

	defTokenRef := config.DefaultAuthTokenRef(provider, refName)
	tokenStorage, err := p.PromptLine(ctx, "Token storage (file path or secret ref)", defTokenRef)
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
