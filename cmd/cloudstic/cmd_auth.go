package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cloudstic/cli/internal/onboarding"
	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/open"
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
	name, err := onboarding.Resolve(ctx, prompterFor(r), a.name, onboarding.Field{
		Label:    "Auth reference name",
		Noun:     "auth reference name",
		Missing:  "-name is required",
		Validate: func(v string) error { return onboarding.ValidateRefName("auth", v) },
	})
	if err != nil {
		return r.fail("%v", err)
	}
	a.name = name
	if a.provider != config.ProviderGoogle && a.provider != config.ProviderOneDrive {
		if r.canPrompt() {
			picked, err := r.promptSelect(ctx, "Select auth provider", []string{config.ProviderGoogle, config.ProviderOneDrive})
			if err != nil {
				return r.fail("Failed to read auth provider: %v", err)
			}
			a.provider = picked
		}
		if a.provider != config.ProviderGoogle && a.provider != config.ProviderOneDrive {
			return r.fail("-provider must be 'google' or 'onedrive'")
		}
	}

	auth := profile.Auth{Provider: a.provider}
	if a.provider == config.ProviderGoogle {
		if a.googleTokenFile == "" && a.googleTokenRef == "" {
			def := defaultAuthTokenRef(config.ProviderGoogle, a.name)
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
	if a.provider == config.ProviderOneDrive {
		if a.onedriveTokenFile == "" && a.onedriveTokenRef == "" {
			def := defaultAuthTokenRef(config.ProviderOneDrive, a.name)
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

	cfg, err := profile.LoadOrEmpty(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	profile.EnsureMaps(cfg)
	cfg.Auth[a.name] = auth

	if err := profile.Save(a.profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}
	_, _ = fmt.Fprintf(r.out, "Auth %q saved in %s\n", a.name, a.profilesFile)
	return 0
}

type authLoginArgs struct {
	*globalFlags
	profilesFile     string
	name             string
	googleCreds      string
	googleCredsRef   string
	googleCredsJSON  string
	onedriveClientID string
}

func declareAuthLoginArgs(g *globalFlags) (*authLoginArgs, commandInput) {
	a := &authLoginArgs{globalFlags: g}
	return a, commandInput{
		flags: []flagSpec{
			profilesFileFlag(&a.profilesFile, g),
			stringFlag(&a.name, "name", "", "Auth reference name", withPlaceholder("<name>"), withCompleter("_cloudstic_auth_names")),
			// Which OAuth client to authorize with. The entry normally says,
			// but these let a caller supply one it does not have — and carry
			// the environment bindings that make a build without the
			// ldflags-injected default client usable at all.
			stringFlag(&a.googleCreds, "google-credentials", "", "Path to Google OAuth client or service account credentials JSON file",
				withEnv("GOOGLE_APPLICATION_CREDENTIALS"), withPlaceholder("<path>"), withCompleter("_files")),
			stringFlag(&a.googleCredsRef, "google-credentials-ref", "", "Secret reference to Google credentials JSON",
				withPlaceholder("<ref>")),
			stringFlag(&a.googleCredsJSON, "google-credentials-json", "", "Inline Google credentials JSON",
				withEnv("GOOGLE_CREDENTIALS_JSON"), withPlaceholder("<json>"), asSecret()),
			stringFlag(&a.onedriveClientID, "onedrive-client-id", "", "OneDrive OAuth client ID",
				withEnv("ONEDRIVE_CLIENT_ID"), withPlaceholder("<id>")),
		},
		positionals: []positionalSpec{optionalPositional(&a.name, "auth name", "", "_cloudstic_auth_names")},
	}
}

// applyAuthLoginCredentialFlags folds this command's credential flags over the
// auth entry's own values.
//
// The precedence is the one the rest of the CLI applies (see the
// resolution-precedence note in config.go): an explicitly passed flag wins, then
// what the auth entry says, then an environment value. That ordering is why this
// consults flagProvided rather than just checking for a non-empty flag —
// an environment value must not override an entry that names something
// different, but must still fill an entry that names nothing.
//
// The environment bindings are what make a locally built binary usable: the
// built-in OAuth client is injected via ldflags at release time, so a plain
// `go build` has an empty client ID and any authorization it starts is rejected
// for having no client_id at all.
func applyAuthLoginCredentialFlags(cfg *config.Source, a *authLoginArgs) {
	fold := func(flag, val string, dest *string) {
		if val == "" {
			return
		}
		if a.flagProvided(flag) || *dest == "" {
			*dest = val
		}
	}
	fold("google-credentials", a.googleCreds, &cfg.Google.CredsPath)
	fold("google-credentials-ref", a.googleCredsRef, &cfg.Google.CredsRef)
	fold("google-credentials-json", a.googleCredsJSON, &cfg.Google.CredsJSON)
	fold("onedrive-client-id", a.onedriveClientID, &cfg.OneDrive.ClientID)
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

	srcCfg, err := config.SourceForAuth(auth)
	if err != nil {
		return r.fail("Auth %q: %v", a.name, err)
	}
	srcCfg.ConfigDir = a.configDir
	applyAuthLoginCredentialFlags(&srcCfg, a)

	// The browser banner goes to errOut, where this CLI puts everything a human
	// reads while stdout carries data — so `auth login -json` stays a clean
	// stream.
	src, err := open.Source(ctx, srcCfg,
		open.WithSecretResolver(newSecretResolver(a.configDir)),
		open.WithPromptWriter(r.errOut))
	if err != nil {
		return r.fail("Failed to initialize auth source: %v", err)
	}

	info := src.Info()

	_, _ = fmt.Fprintf(r.out, "Successfully logged in as %s (%s)\n", info.Account, info.Type)
	return 0
}

// defaultAuthTokenRef is config.DefaultAuthTokenRef under the name this package
// has always used for it. The convention itself lives in pkg/config so that
// anything writing an auth entry produces the same reference the CLI reads.
func defaultAuthTokenRef(provider, name string) string {
	return config.DefaultAuthTokenRef(provider, name)
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
