// Store reachability and initialization, including the AWS SSO re-auth path.
//
// Split from cmd_store.go, which declares the store subcommands. `store verify`,
// `store init`, `profile new` and `setup` all need to answer "is this store
// reachable, and is it initialized?", so the answer lives here rather than in
// any one command's file.
package main

import (
	"context"
	"fmt"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/profile"
	"github.com/cloudstic/cli/pkg/store"
)

type checkOrInitOptions struct {
	// configDir is the resolved -config-dir, which the store's secret
	// references are read relative to. See newSecretResolver.
	configDir            string
	allowMissingSecrets  bool
	warnOnMissingSecrets bool
	offerInit            bool
	assumeYes            bool
}

func checkOrInitStore(r *runner, ctx context.Context, cfg *profile.Config, storeName, profilesFile string, opts checkOrInitOptions) error {
	s := cfg.Stores[storeName]
	resolved, err := clientConfigFromProfileStore(s, opts.configDir)
	if err != nil {
		if opts.allowMissingSecrets && isSecretNotFoundError(err) {
			if opts.warnOnMissingSecrets {
				_, _ = fmt.Fprintf(r.errOut, "Store credentials are configured but not currently available: %v\n", err)
				_, _ = fmt.Fprintf(r.errOut, "Set required secrets and run: cloudstic store verify %s\n", storeName)
			}
			return nil
		}
		return fmt.Errorf("could not resolve store credentials: %w", err)
	}
	resolved.Unlock.NoPrompt = r.noPrompt
	raw, err := openStore(ctx, resolved.Store)
	if err != nil {
		return fmt.Errorf("could not connect to store: %w", err)
	}

	// Check if already initialized by looking for the config marker.
	cfgData, err := raw.Get(ctx, "config")
	if err == nil && cfgData != nil {
		_, _ = fmt.Fprintln(r.out, "Store is already initialized and accessible.")
		// InspectRepo, not LoadRepoConfig: an encrypted repository's marker is
		// sealed, and this runs before any credentials have been verified —
		// precisely to decide whether they need to be.
		repoStatus, cfgErr := cloudstic.InspectRepo(ctx, raw)
		if cfgErr != nil {
			return fmt.Errorf("read repository config: %w", cfgErr)
		}
		if repoStatus.Encrypted {
			_, _ = fmt.Fprintln(r.out, "Repository is encrypted; verifying configured credentials...")
			if err := verifyStoreEncryptionCredentials(ctx, resolved.Unlock, raw); err != nil {
				return fmt.Errorf("store is initialized, but configured encryption credentials are invalid: %w", err)
			}
			_, _ = fmt.Fprintln(r.out, "Encryption credentials are valid.")
		}
		return nil
	}

	_, _ = fmt.Fprintln(r.out, "Store is accessible but not yet initialized.")
	if !opts.offerInit {
		return nil
	}
	if !opts.assumeYes {
		yes, promptErr := r.promptConfirm(ctx, "Initialize it now?", true)
		if promptErr != nil || !yes {
			return nil
		}
	}

	// Check if the store has encryption config.
	hasEncryption := storeHasExplicitEncryption(s)

	if !hasEncryption {
		// No encryption configured — init without encryption.
		result, initErr := cloudstic.InitRepo(ctx, raw, cloudstic.WithInitNoEncryption())
		if initErr != nil {
			return fmt.Errorf("init failed: %w", initErr)
		}
		printInitResult(r.errOut, result)
		return nil
	}

	// Build keychain from the store's encryption settings.
	// For password-based encryption, the env var must be set for init.
	// If not set, prompt for the password interactively.
	kc, err := buildKeychain(ctx, resolved.Unlock)
	if err != nil {
		return fmt.Errorf("failed to build keychain: %w", err)
	}

	var initOpts []cloudstic.InitOption
	initOpts = append(initOpts, cloudstic.WithInitCredentials(kc))
	result, err := cloudstic.InitRepo(ctx, raw, initOpts...)
	if err != nil {
		return fmt.Errorf("init failed: %w", err)
	}
	printInitResult(r.errOut, result)
	return nil
}

func checkOrInitStoreWithRecovery(r *runner, ctx context.Context, cfg *profile.Config, storeName, profilesFile string, opts checkOrInitOptions, allowSkip bool) error {
	for {
		err := checkOrInitStore(r, ctx, cfg, storeName, profilesFile, opts)
		if err == nil || !r.canPrompt() {
			return err
		}

		s := cfg.Stores[storeName]
		options := []string{"Retry"}
		loginOption := awsSSOLoginOption(s, err)
		if loginOption != "" {
			options = append(options, loginOption)
		}
		if allowSkip {
			options = append(options, "Skip for now")
		} else {
			options = append(options, "Abort")
		}

		_, _ = fmt.Fprintf(r.errOut, "%v\n", err)
		picked, promptErr := r.promptSelect(ctx, "Store verification failed", options)
		if promptErr != nil {
			return err
		}

		switch picked {
		case "Retry":
			continue
		case "Skip for now":
			return nil
		case "Abort":
			return err
		case loginOption:
			if runErr := runAWSSSOLogin(r, ctx, s); runErr != nil {
				_, _ = fmt.Fprintf(r.errOut, "AWS SSO login failed: %v\n", runErr)
			}
		default:
			return err
		}
	}
}

func verifyStoreEncryptionCredentials(ctx context.Context, cfg unlockConfig, raw store.ObjectStore) error {
	kc, err := buildKeychain(ctx, cfg)
	if err != nil {
		return fmt.Errorf("build keychain: %w", err)
	}
	_, err = cloudstic.NewClient(ctx, raw,
		cloudstic.WithKeychain(kc),
		cloudstic.WithReporter(ui.NewNoOpReporter()),
	)
	if err != nil {
		return err
	}
	return nil
}

func awsSSOLoginOption(s profile.Store, err error) string {
	if !isAWSExpiredAuthError(err) {
		return ""
	}
	if s.S3Profile != "" {
		return fmt.Sprintf("Run aws sso login --profile %s", s.S3Profile)
	}
	return "Run aws sso login"
}

func isAWSExpiredAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"sso session has expired",
		"sso session is expired",
		"sso session is invalid",
		"ssoproviderinvalidtoken",
		"token has expired and refresh failed",
		"the security token included in the request is expired",
		"expiredtoken",
		"expired token",
		"invalid security token",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func runAWSSSOLogin(r *runner, ctx context.Context, s profile.Store) error {
	args := []string{"sso", "login"}
	if s.S3Profile != "" {
		args = append(args, "--profile", s.S3Profile)
	}
	_, _ = fmt.Fprintf(r.errOut, "Running: aws %s\n", strings.Join(args, " "))
	runFn := r.runInteractiveCmd
	if runFn == nil {
		runFn = defaultRunInteractiveCmd
	}
	return runFn(ctx, r.stdin, r.out, r.errOut, "aws", args...)
}
