package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/paths"
)

func (r *runner) runAuth() int {
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic auth <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: list, show, new, login")
		return 1
	}

	switch os.Args[2] {
	case "list":
		return r.runAuthList()
	case "show":
		return r.runAuthShow()
	case "new":
		return r.runAuthNew()
	case "login":
		return r.runAuthLogin()
	default:
		return r.fail("Unknown auth subcommand: %s", os.Args[2])
	}
}

func (r *runner) runAuthList() int {
	fs := flag.NewFlagSet("auth list", flag.ExitOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return r.fail("Failed to load profiles: %v", err)
	}

	r.renderAuthList(cfg)
	return 0
}

func (r *runner) runAuthShow() int {
	fs := flag.NewFlagSet("auth show", flag.ExitOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))
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
		picked, pickErr := r.promptSelect("Select auth entry", names)
		if pickErr != nil {
			return r.fail("Failed to select auth entry: %v", pickErr)
		}
		name = picked
	}

	auth, ok := cfg.Auth[name]
	if !ok {
		return r.fail("Unknown auth %q", name)
	}
	r.renderAuthShow(cfg, name, auth)
	return 0
}

func (r *runner) runAuthNew() int {
	fs := flag.NewFlagSet("auth new", flag.ExitOnError)
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
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))

	if *name == "" {
		if r.canPrompt() {
			v, err := r.promptLine("Auth reference name", "")
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
			picked, err := r.promptSelect("Select auth provider", []string{"google", "onedrive"})
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
			if r.canPrompt() {
				// We still default to file if not interactive, but we could offer keychain here too.
				// For now let's keep it simple and add the fields.
				def := defaultAuthTokenPath("google", *name)
				v, err := r.promptLine("Google token file path (or keychain://...)", def)
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
				*googleTokenFile = defaultAuthTokenPath("google", *name)
			}
		}
		auth.GoogleCreds = *googleCreds
		auth.GoogleCredsRef = *googleCredsRef
		auth.GoogleTokenFile = *googleTokenFile
		auth.GoogleTokenRef = *googleTokenRef
	}
	if *provider == "onedrive" {
		if *onedriveTokenFile == "" && *onedriveTokenRef == "" {
			if r.canPrompt() {
				def := defaultAuthTokenPath("onedrive", *name)
				v, err := r.promptLine("OneDrive token file path (or keychain://...)", def)
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
				*onedriveTokenFile = defaultAuthTokenPath("onedrive", *name)
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

func (r *runner) runAuthLogin() int {
	fs := flag.NewFlagSet("auth login", flag.ExitOnError)
	profilesFile := fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback()), "Path to profiles YAML file")
	name := fs.String("name", "", "Auth reference name")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))

	cfg, err := cloudstic.LoadProfilesFile(*profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}

	if *name == "" {
		if r.canPrompt() {
			names := sortedKeys(cfg.Auth)
			picked, pickErr := r.promptSelect("Select auth entry", names)
			if pickErr != nil {
				return r.fail("Failed to select auth entry: %v", pickErr)
			}
			*name = picked
		}
		if *name == "" {
			return r.fail("-name is required")
		}
	}

	auth, ok := cfg.Auth[*name]
	if !ok {
		return r.fail("Unknown auth %q", *name)
	}

	g := newAuthGlobalFlags()
	ctx := context.Background()

	switch auth.Provider {
	case "google":
		googleCreds := auth.GoogleCreds
		if googleCreds == "" && auth.GoogleCredsRef == "" {
			googleCreds = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		}
		src, err := initSource(ctx, initSourceOptions{
			sourceURI:       "gdrive:/",
			googleCreds:     googleCreds,
			googleCredsRef:  auth.GoogleCredsRef,
			googleTokenFile: auth.GoogleTokenFile,
			googleTokenRef:  auth.GoogleTokenRef,
			globalFlags:     g,
		})
		if err != nil {
			return r.fail("Failed to initialize Google auth source: %v", err)
		}
		_ = src.Info()
	case "onedrive":
		onedriveClientID := auth.OneDriveClientID
		if onedriveClientID == "" {
			onedriveClientID = os.Getenv("ONEDRIVE_CLIENT_ID")
		}
		src, err := initSource(ctx, initSourceOptions{
			sourceURI:         "onedrive:/",
			onedriveClientID:  onedriveClientID,
			onedriveTokenFile: auth.OneDriveTokenFile,
			onedriveTokenRef:  auth.OneDriveTokenRef,
			globalFlags:       g,
		})
		if err != nil {
			return r.fail("Failed to initialize OneDrive auth source: %v", err)
		}
		_ = src.Info()
	default:
		return r.fail("Unsupported auth provider %q", auth.Provider)
	}

	_, _ = fmt.Fprintf(r.out, "Auth %q is ready\n", *name)
	return 0
}

func newAuthGlobalFlags() *globalFlags {
	fs := flag.NewFlagSet("auth-login-flags", flag.ContinueOnError)
	return addGlobalFlags(fs)
}

func defaultAuthTokenPath(provider, name string) string {
	configDir, err := paths.ConfigDir()
	if err != nil {
		return ""
	}
	safeName := strings.ReplaceAll(strings.TrimSpace(name), " ", "-")
	if safeName == "" {
		safeName = "default"
	}
	file := fmt.Sprintf("%s-%s_token.json", provider, safeName)
	return filepath.Join(configDir, "tokens", file)
}
