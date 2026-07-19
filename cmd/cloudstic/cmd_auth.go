package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	cloudstic "github.com/cloudstic/cli"
)

func authCommandSpec() *commandSpec {
	return group("auth", "Manage reusable cloud auth entries",
		authNewCommandSpec(), authListCommandSpec(), authShowCommandSpec(), authLoginCommandSpec())
}

func authNewCommandSpec() *commandSpec {
	return leaf("new", "Create or update a reusable cloud auth entry", "", runAuthNew,
		profilesFileFlag(), valueFlag("name", "name", "Auth reference name", completionNone),
		valueFlag("provider", "provider", "Auth provider", completionNone), googleCredentialsFlag(),
		valueFlag("google-credentials-ref", "ref", "Google credentials reference", completionNone), googleTokenFileFlag(),
		valueFlag("google-token-ref", "ref", "Google token reference", completionNone), onedriveClientIDFlag(), onedriveTokenFileFlag(),
		valueFlag("onedrive-token-ref", "ref", "OneDrive token reference", completionNone))
}

func authListCommandSpec() *commandSpec {
	return leaf("list", "List configured auth entries", "", runAuthList, profilesFileFlag())
}

func authShowCommandSpec() *commandSpec {
	return leaf("show", "Show one auth entry", "<name>", runAuthShow, profilesFileFlag())
}

func authLoginCommandSpec() *commandSpec {
	return leaf("login", "Run OAuth login for one auth entry", "", runAuthLogin,
		profilesFileFlag(), valueFlag("name", "name", "Auth reference name", completionAuth))
}

func runAuthList(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("auth list", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", defaultProfilesPathFallback(), "Path to profiles YAML file")
	if err := parseFlags(fs, r.args, authListCommandSpec()); err != nil {
		return r.parseError(err)
	}

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	renderAuthList(r.out, cfg)
	return 0
}

func runAuthShow(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("auth show", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", defaultProfilesPathFallback(), "Path to profiles YAML file")
	if err := parseFlags(fs, r.args, authShowCommandSpec()); err != nil {
		return r.parseError(err)
	}
	if fs.NArg() > 1 {
		return r.fail("usage: cloudstic auth show [-profiles-file <path>] <name>")
	}
	name := ""
	if fs.NArg() == 1 {
		name = fs.Arg(0)
	}

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic auth show [-profiles-file <path>] <name>")
		}
		names := sortedKeys(cfg.Auth)
		picked, pickErr := r.promptSelect(ctx, "Select auth entry", names)
		if pickErr != nil {
			return r.fail("Failed to select auth entry: %v", pickErr)
		}
		name = picked
	}

	auth, ok := cfg.Auth[name]
	if !ok {
		return r.fail("Unknown auth %q", name)
	}
	renderAuthShow(r.out, cfg, name, auth)
	return 0
}

func runAuthNew(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("auth new", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", defaultProfilesPathFallback(), "Path to profiles YAML file")
	name := fs.String("name", "", "Auth reference name")
	provider := fs.String("provider", "", "Auth provider: google|onedrive")
	googleCreds := fs.String("google-credentials", "", "Path to Google service account credentials JSON file")
	googleCredsRef := fs.String("google-credentials-ref", "", "Secret reference to Google service account credentials JSON")
	googleTokenFile := fs.String("google-token-file", "", "Path to Google OAuth token file")
	googleTokenRef := fs.String("google-token-ref", "", "Secret reference to Google OAuth token")
	onedriveClientID := fs.String("onedrive-client-id", "", "OneDrive OAuth client ID")
	onedriveTokenFile := fs.String("onedrive-token-file", "", "Path to OneDrive OAuth token file")
	onedriveTokenRef := fs.String("onedrive-token-ref", "", "Secret reference to OneDrive OAuth token")
	if err := parseFlags(fs, r.args, authNewCommandSpec()); err != nil {
		return r.parseError(err)
	}

	if *name == "" {
		if r.canPrompt() {
			v, err := r.promptValidatedLine(ctx, "Auth reference name", "", func(v string) error {
				if v == "" {
					return fmt.Errorf("auth reference name is required")
				}
				return validateRefName("auth", v)
			})
			if err != nil {
				return r.fail("Failed to read auth reference name: %v", err)
			}
			*name = v
		}
		if *name == "" {
			return r.fail("-name is required")
		}
	}
	if err := validateRefName("auth", *name); err != nil {
		return r.fail("%v", err)
	}
	if *provider != "google" && *provider != "onedrive" {
		if r.canPrompt() {
			picked, err := r.promptSelect(ctx, "Select auth provider", []string{"google", "onedrive"})
			if err != nil {
				return r.fail("Failed to read auth provider: %v", err)
			}
			*provider = picked
		}
		if *provider != "google" && *provider != "onedrive" {
			return r.fail("-provider must be 'google' or 'onedrive'")
		}
	}

	auth := cloudstic.ProfileAuth{Provider: *provider}
	if *provider == "google" {
		if *googleTokenFile == "" && *googleTokenRef == "" {
			def := defaultAuthTokenRef("google", *name)
			if r.canPrompt() {
				v, err := r.promptLine(ctx, "Google token storage (file path or secret ref)", def)
				if err != nil {
					return r.fail("Failed to read google token storage: %v", err)
				}
				if strings.Contains(v, "://") {
					*googleTokenRef = v
				} else {
					*googleTokenFile = v
				}
			}
			if *googleTokenFile == "" && *googleTokenRef == "" {
				*googleTokenRef = def
			}
		}
		auth.GoogleCreds = *googleCreds
		auth.GoogleCredsRef = *googleCredsRef
		auth.GoogleTokenFile = *googleTokenFile
		auth.GoogleTokenRef = *googleTokenRef
	}
	if *provider == "onedrive" {
		if *onedriveTokenFile == "" && *onedriveTokenRef == "" {
			def := defaultAuthTokenRef("onedrive", *name)
			if r.canPrompt() {
				v, err := r.promptLine(ctx, "OneDrive token storage (file path or secret ref)", def)
				if err != nil {
					return r.fail("Failed to read onedrive token storage: %v", err)
				}
				if strings.Contains(v, "://") {
					*onedriveTokenRef = v
				} else {
					*onedriveTokenFile = v
				}
			}
			if *onedriveTokenFile == "" && *onedriveTokenRef == "" {
				*onedriveTokenRef = def
			}
		}
		auth.OneDriveClientID = *onedriveClientID
		auth.OneDriveTokenFile = *onedriveTokenFile
		auth.OneDriveTokenRef = *onedriveTokenRef
	}

	cfg, err := loadProfilesOrInit(*profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	ensureProfilesMaps(cfg)
	cfg.Auth[*name] = auth

	if err := cloudstic.SaveProfilesFile(*profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}
	_, _ = fmt.Fprintf(r.out, "Auth %q saved in %s\n", *name, *profilesFile)
	return 0
}

func runAuthLogin(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", defaultProfilesPathFallback(), "Path to profiles YAML file")
	name := fs.String("name", "", "Auth reference name")
	if err := parseFlags(fs, r.args, authLoginCommandSpec()); err != nil {
		return r.parseError(err)
	}

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if *name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic auth login [-profiles-file <path>] <name>")
		}
		names := sortedKeys(cfg.Auth)
		picked, pickErr := r.promptSelect(ctx, "Select auth entry", names)
		if pickErr != nil {
			return r.fail("Failed to select auth entry: %v", pickErr)
		}
		*name = picked
	}

	auth, ok := cfg.Auth[*name]
	if !ok {
		return r.fail("Unknown auth %q", *name)
	}

	src, err := initSource(ctx, initSourceOptions{
		sourceURI:         auth.Provider + "://auth",
		googleCreds:       auth.GoogleCreds,
		googleCredsRef:    auth.GoogleCredsRef,
		googleTokenFile:   auth.GoogleTokenFile,
		googleTokenRef:    auth.GoogleTokenRef,
		onedriveClientID:  auth.OneDriveClientID,
		onedriveTokenFile: auth.OneDriveTokenFile,
		onedriveTokenRef:  auth.OneDriveTokenRef,
		globalFlags:       &globalFlags{}, // dummy
	})
	if err != nil {
		return r.fail("Failed to initialize auth source: %v", err)
	}

	info := src.Info()

	_, _ = fmt.Fprintf(r.out, "Successfully logged in as %s (%s)\n", info.Account, info.Type)
	return 0
}

func defaultAuthTokenRef(provider, name string) string {
	if name == "" {
		name = "default"
	}
	return "config-token://" + provider + "/" + name
}
