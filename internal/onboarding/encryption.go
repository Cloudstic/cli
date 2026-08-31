// Store encryption configuration: choosing a method for a store and recording
// where its secrets live.
//
// The selection and the secret-reference walk were already decoupled from the
// CLI — both took their prompts as function parameters — but they sat in
// cmd/cloudstic, so their tests still had to build a runner. Here they need
// only a Prompter (issue #570).
package onboarding

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cloudstic/cli/pkg/profile"
	"github.com/cloudstic/cli/pkg/secretref"
)

// promptEncryptionConfig guides the user through encryption configuration
// and saves the chosen settings to profiles.yaml. It does not build a keychain
// or prompt for the actual password — that happens later during init.
// promptEncryptionConfig walks the user through choosing an encryption method for
// a store and records the choice in the profiles file.
//
// A free function taking the runner: it reads and writes a profiles file and
// mutates cfg, which is profiles-domain work rather than a runner capability.
func ConfigureEncryption(ctx context.Context, p Prompter, cfg *profile.Config, storeName, profilesFile string, resolver *secretref.Resolver, out, errOut io.Writer) {
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "No encryption is configured for this store.")

	options := []string{
		"Password (recommended for interactive use)",
		"Platform key (recommended for automation/CI)",
		"AWS KMS key (enterprise)",
		"No encryption (not recommended)",
	}
	picked, err := p.PromptSelect(ctx, "Select encryption method", options)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "Failed to select encryption method: %v\n", err)
		return
	}

	s, err := configureSelection(
		ctx,
		cfg.Stores[storeName],
		storeName,
		picked,
		func(ctx context.Context, storeName, secretLabel, defaultEnvName, defaultAccount string) (string, error) {
			return secretReference(ctx, storeName, secretLabel, defaultEnvName, defaultAccount,
				p.PromptSelect, p.PromptLine,
				func(_ context.Context, label string) (string, error) { return p.PromptSecret(ctx, label) },
				os.LookupEnv, resolver)
		},
		p.PromptLine,
		out,
	)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "%v\n", err)
		return
	}
	if picked == options[3] {
		return
	}

	// Save updated store config.
	cfg.Stores[storeName] = s
	if saveErr := profile.Save(profilesFile, cfg); saveErr != nil {
		_, _ = fmt.Fprintf(errOut, "Warning: could not save encryption settings: %v\n", saveErr)
	}
}

func configureSelection(
	ctx context.Context,
	s profile.Store,
	storeName, picked string,
	promptSecretRef func(context.Context, string, string, string, string) (string, error),
	promptLine func(context.Context, string, string) (string, error),
	out io.Writer,
) (profile.Store, error) {
	switch picked {
	case "Password (recommended for interactive use)":
		secretRef, err := promptSecretRef(ctx, storeName, "repository password", "CLOUDSTIC_PASSWORD", "password")
		if err != nil {
			return s, fmt.Errorf("failed to configure password secret: %w", err)
		}
		s.PasswordSecret = secretRef
		_, _ = fmt.Fprintf(out, "Encryption: password via %s\n", secretRef)
	case "Platform key (recommended for automation/CI)":
		secretRef, err := promptSecretRef(ctx, storeName, "platform key (64-char hex)", "CLOUDSTIC_ENCRYPTION_KEY", "encryption-key")
		if err != nil {
			return s, fmt.Errorf("failed to configure platform key secret: %w", err)
		}
		s.EncryptionKeySecret = secretRef
		_, _ = fmt.Fprintf(out, "Encryption: platform key via %s\n", secretRef)
	case "AWS KMS key (enterprise)":
		arn, err := promptLine(ctx, "KMS key ARN", "")
		if err != nil || arn == "" {
			return s, fmt.Errorf("KMS key ARN is required")
		}
		s.KMSKeyARN = arn
		region, _ := promptLine(ctx, "KMS region", "us-east-1")
		if region != "" {
			s.KMSRegion = region
		}
		_, _ = fmt.Fprintf(out, "Encryption: AWS KMS (%s)\n", arn)
	case "No encryption (not recommended)":
		_, _ = fmt.Fprintln(out, "Encryption: none (not recommended)")
	default:
		return s, fmt.Errorf("unsupported encryption selection: %s", picked)
	}
	return s, nil
}

func secretReference(
	ctx context.Context,
	storeName, secretLabel, defaultEnvName, defaultAccount string,
	promptSelect func(context.Context, string, []string) (string, error),
	promptLine func(context.Context, string, string) (string, error),
	promptSecret func(context.Context, string) (string, error),
	lookupEnv func(string) (string, bool),
	resolver *secretref.Resolver,
) (string, error) {
	writableBackends := resolver.WritableBackends()
	nativeRef := func(backend secretref.WritableBackend) (string, error) {
		ref := backend.DefaultRef(storeName, defaultAccount)
		exists, err := resolver.Exists(ctx, ref)
		if err != nil {
			return "", err
		}
		if exists {
			return ref, nil
		}
		secretValue, err := promptSecret(ctx, "Secret value")
		if err != nil {
			return "", err
		}
		if secretValue == "" {
			return "", fmt.Errorf("secret value cannot be empty")
		}
		if err := resolver.Store(ctx, ref, secretValue); err != nil {
			return "", err
		}
		return ref, nil
	}

	if len(writableBackends) > 0 {
		options := []string{"Environment variable (env://)"}
		backendByOption := map[string]secretref.WritableBackend{}
		for _, backend := range writableBackends {
			option := fmt.Sprintf("%s (%s://)", backend.DisplayName(), backend.Scheme())
			options = append(options, option)
			backendByOption[option] = backend
		}
		picked, err := promptSelect(
			ctx,
			fmt.Sprintf("Where should %s be stored?", secretLabel),
			options,
		)
		if err != nil {
			return "", err
		}
		if backend, ok := backendByOption[picked]; ok {
			return nativeRef(backend)
		}
	}

	envName, err := promptLine(ctx, "Env var name", defaultEnvName)
	if err != nil {
		return "", err
	}
	if _, ok := lookupEnv(envName); !ok && len(writableBackends) > 0 {
		options := []string{"Keep environment variable reference (env://)"}
		backendByOption := map[string]secretref.WritableBackend{}
		for _, backend := range writableBackends {
			option := fmt.Sprintf("Store in %s instead (%s://)", backend.DisplayName(), backend.Scheme())
			options = append(options, option)
			backendByOption[option] = backend
		}
		picked, err := promptSelect(
			ctx,
			fmt.Sprintf("Environment variable %q is not set in this shell", envName),
			options,
		)
		if err != nil {
			return "", err
		}
		if backend, ok := backendByOption[picked]; ok {
			return nativeRef(backend)
		}
	}
	return envRef(envName), nil
}

func envRef(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "env://" + name
}

func HasExplicitEncryption(s profile.Store) bool {
	return s.PasswordSecret != "" || s.EncryptionKeySecret != "" || s.RecoveryKeySecret != "" ||
		s.KMSKeyARN != ""
}
