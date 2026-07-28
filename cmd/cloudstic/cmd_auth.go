package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cloudstic/cli/pkg/profile"
)

type authListArgs struct{ profilesFile string }

func declareAuthListArgs(g *globalFlags) (*authListArgs, commandInput) {
	a := &authListArgs{}
	return a, commandInput{flags: []flagSpec{profilesFileFlag(&a.profilesFile, g)}}
}

func runAuthList(r *runner, ctx context.Context, a *authListArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	renderAuthList(r.out, cfg)
	return 0
}

type authShowArgs struct {
	profilesFile string
	name         string
}

func declareAuthShowArgs(g *globalFlags) (*authShowArgs, commandInput) {
	a := &authShowArgs{}
	return a, commandInput{
		flags:       []flagSpec{profilesFileFlag(&a.profilesFile, g)},
		positionals: []positionalSpec{optionalPositional(&a.name, "auth name", "", "_cloudstic_auth_names")},
	}
}

func runAuthShow(r *runner, ctx context.Context, a *authShowArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if a.name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic auth show [-profiles-file <path>] <name>")
		}
		names := sortedKeys(cfg.Auth)
		picked, pickErr := r.promptSelect(ctx, "Select auth entry", names)
		if pickErr != nil {
			return r.fail("Failed to select auth entry: %v", pickErr)
		}
		a.name = picked
	}

	auth, ok := cfg.Auth[a.name]
	if !ok {
		return r.fail("Unknown auth %q", a.name)
	}
	renderAuthShow(r.out, cfg, a.name, auth)
	return 0
}

type authNewArgs struct {
	profilesFile      string
	name              string
	provider          string
	googleCreds       string
	googleCredsRef    string
	googleTokenFile   string
	googleTokenRef    string
	onedriveClientID  string
	onedriveTokenFile string
	onedriveTokenRef  string
}

func declareAuthNewArgs(g *globalFlags) (*authNewArgs, commandInput) {
	a := &authNewArgs{}
	return a, commandInput{flags: []flagSpec{
		profilesFileFlag(&a.profilesFile, g),
		stringFlag(&a.name, "name", "", "Auth reference name", withPlaceholder("<name>")),
		stringFlag(&a.provider, "provider", "", "Auth provider: google|onedrive", withPlaceholder("<google|onedrive>")),
		stringFlag(&a.googleCreds, "google-credentials", "", "Path to Google service account credentials JSON file", withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.googleCredsRef, "google-credentials-ref", "", "Secret reference to Google service account credentials JSON", withPlaceholder("<ref>")),
		stringFlag(&a.googleTokenFile, "google-token-file", "", "Path to Google OAuth token file", withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.googleTokenRef, "google-token-ref", "", "Secret reference to Google OAuth token", withPlaceholder("<ref>")),
		stringFlag(&a.onedriveClientID, "onedrive-client-id", "", "OneDrive OAuth client ID", withPlaceholder("<id>")),
		stringFlag(&a.onedriveTokenFile, "onedrive-token-file", "", "Path to OneDrive OAuth token file", withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.onedriveTokenRef, "onedrive-token-ref", "", "Secret reference to OneDrive OAuth token", withPlaceholder("<ref>")),
	}}
}

func runAuthNew(r *runner, ctx context.Context, a *authNewArgs) int {
	if a.name == "" {
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
			a.name = v
		}
		if a.name == "" {
			return r.fail("-name is required")
		}
	}
	if err := validateRefName("auth", a.name); err != nil {
		return r.fail("%v", err)
	}
	if a.provider != "google" && a.provider != "onedrive" {
		if r.canPrompt() {
			picked, err := r.promptSelect(ctx, "Select auth provider", []string{"google", "onedrive"})
			if err != nil {
				return r.fail("Failed to read auth provider: %v", err)
			}
			a.provider = picked
		}
		if a.provider != "google" && a.provider != "onedrive" {
			return r.fail("-provider must be 'google' or 'onedrive'")
		}
	}

	auth := profile.Auth{Provider: a.provider}
	if a.provider == "google" {
		if a.googleTokenFile == "" && a.googleTokenRef == "" {
			def := defaultAuthTokenRef("google", a.name)
			if r.canPrompt() {
				v, err := r.promptLine(ctx, "Google token storage (file path or secret ref)", def)
				if err != nil {
					return r.fail("Failed to read google token storage: %v", err)
				}
				if strings.Contains(v, "://") {
					a.googleTokenRef = v
				} else {
					a.googleTokenFile = v
				}
			}
			if a.googleTokenFile == "" && a.googleTokenRef == "" {
				a.googleTokenRef = def
			}
		}
		auth.GoogleCreds = a.googleCreds
		auth.GoogleCredsRef = a.googleCredsRef
		auth.GoogleTokenFile = a.googleTokenFile
		auth.GoogleTokenRef = a.googleTokenRef
	}
	if a.provider == "onedrive" {
		if a.onedriveTokenFile == "" && a.onedriveTokenRef == "" {
			def := defaultAuthTokenRef("onedrive", a.name)
			if r.canPrompt() {
				v, err := r.promptLine(ctx, "OneDrive token storage (file path or secret ref)", def)
				if err != nil {
					return r.fail("Failed to read onedrive token storage: %v", err)
				}
				if strings.Contains(v, "://") {
					a.onedriveTokenRef = v
				} else {
					a.onedriveTokenFile = v
				}
			}
			if a.onedriveTokenFile == "" && a.onedriveTokenRef == "" {
				a.onedriveTokenRef = def
			}
		}
		auth.OneDriveClientID = a.onedriveClientID
		auth.OneDriveTokenFile = a.onedriveTokenFile
		auth.OneDriveTokenRef = a.onedriveTokenRef
	}

	cfg, err := loadProfilesOrInit(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	ensureProfilesMaps(cfg)
	cfg.Auth[a.name] = auth

	if err := profile.Save(a.profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}
	_, _ = fmt.Fprintf(r.out, "Auth %q saved in %s\n", a.name, a.profilesFile)
	return 0
}

type authLoginArgs struct {
	*globalFlags
	profilesFile string
	name         string
}

func declareAuthLoginArgs(g *globalFlags) (*authLoginArgs, commandInput) {
	a := &authLoginArgs{globalFlags: g}
	return a, commandInput{
		flags: []flagSpec{
			profilesFileFlag(&a.profilesFile, g),
			stringFlag(&a.name, "name", "", "Auth reference name", withPlaceholder("<name>"), withCompleter("_cloudstic_auth_names")),
		},
		positionals: []positionalSpec{optionalPositional(&a.name, "auth name", "", "_cloudstic_auth_names")},
	}
}

func runAuthLogin(r *runner, ctx context.Context, a *authLoginArgs) int {
	cfg, err := profile.Load(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if a.name == "" {
		if !r.canPrompt() {
			return r.fail("usage: cloudstic auth login [-profiles-file <path>] <name>")
		}
		names := sortedKeys(cfg.Auth)
		picked, pickErr := r.promptSelect(ctx, "Select auth entry", names)
		if pickErr != nil {
			return r.fail("Failed to select auth entry: %v", pickErr)
		}
		a.name = picked
	}

	auth, ok := cfg.Auth[a.name]
	if !ok {
		return r.fail("Unknown auth %q", a.name)
	}

	src, err := initSource(ctx, initSourceOptions{
		sourceURI:         auth.Provider + "://auth",
		configDir:         a.configDir,
		googleCreds:       auth.GoogleCreds,
		googleCredsRef:    auth.GoogleCredsRef,
		googleTokenFile:   auth.GoogleTokenFile,
		googleTokenRef:    auth.GoogleTokenRef,
		onedriveClientID:  auth.OneDriveClientID,
		onedriveTokenFile: auth.OneDriveTokenFile,
		onedriveTokenRef:  auth.OneDriveTokenRef,
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

// authCommand declares the `auth` command group.
func authCommand() command {
	return group("auth", "Manage reusable cloud auth entries",
		leaf("new", "Create or update a reusable cloud auth entry", nil, declareAuthNewArgs, runAuthNew),
		leaf("list", "List auth entries from profiles.yaml", nil, declareAuthListArgs, runAuthList),
		leaf("show", "Show one auth entry", nil, declareAuthShowArgs, runAuthShow),
		leaf("login", "Run OAuth login flow for one auth entry", nil, declareAuthLoginArgs, runAuthLogin),
	)
}
