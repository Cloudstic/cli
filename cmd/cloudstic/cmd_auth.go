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

func runAuth(r *runner, ctx context.Context) int {
	if len(r.args) < 1 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic auth <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: list, show, new, login")
		return 1
	}

	subcommand := r.args[0]
	subRunner := r.withArgs(r.args[1:])
	switch subcommand {
	case "list":
		return runAuthList(subRunner, ctx)
	case "show":
		return runAuthShow(subRunner, ctx)
	case "new":
		return runAuthNew(subRunner, ctx)
	case "login":
		return runAuthLogin(subRunner, ctx)
	default:
		return r.fail("Unknown auth subcommand: %s", subcommand)
	}
}

func runAuthList(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("auth list", flag.ContinueOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
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
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
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
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	name := fs.String("name", "", "Auth reference name")
	provider := fs.String("provider", "", "Auth provider: google|onedrive")
	googleCreds := fs.String("google-credentials", "", "Path to Google service account credentials JSON file")
	googleCredsRef := fs.String("google-credentials-ref", "", "Secret reference to Google service account credentials JSON")
	googleTokenFile := fs.String("google-token-file", "", "Path to Google OAuth token file")
	googleTokenRef := fs.String("google-token-ref", "", "Secret reference to Google OAuth token")
	onedriveClientID := fs.String("onedrive-client-id", "", "OneDrive OAuth client ID")
	onedriveTokenFile := fs.String("onedrive-token-file", "", "Path to OneDrive OAuth token file")
	onedriveTokenRef := fs.String("onedrive-token-ref", "", "Secret reference to OneDrive OAuth token")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
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
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	name := fs.String("name", "", "Auth reference name")
	if err := parseFlags(boundFlagSet{set: fs}, r.args); err != nil {
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
