package onboarding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"
	"github.com/cloudstic/cli/pkg/secretref"
)

type writableBackendStub struct {
	scheme      string
	displayName string
	defaultRef  string
	exists      func(context.Context, secretref.Ref) (bool, error)
	store       func(context.Context, secretref.Ref, string) error
}

func (b writableBackendStub) Resolve(context.Context, secretref.Ref) (string, error) { return "", nil }
func (b writableBackendStub) Scheme() string                                         { return b.scheme }
func (b writableBackendStub) DisplayName() string                                    { return b.displayName }
func (b writableBackendStub) WriteSupported() bool                                   { return true }
func (b writableBackendStub) DefaultRef(string, string) string                       { return b.defaultRef }
func (b writableBackendStub) Exists(ctx context.Context, ref secretref.Ref) (bool, error) {
	if b.exists == nil {
		return false, nil
	}
	return b.exists(ctx, ref)
}
func (b writableBackendStub) Store(ctx context.Context, ref secretref.Ref, value string) error {
	if b.store == nil {
		return nil
	}
	return b.store(ctx, ref, value)
}

func TestSecretReference_DarwinKeychain(t *testing.T) {
	resolver := secretref.NewResolver(map[string]secretref.Backend{
		"keychain": writableBackendStub{
			scheme:      "keychain",
			displayName: "macOS Keychain",
			defaultRef:  "keychain://cloudstic/store/prod-store/password",
			exists:      func(context.Context, secretref.Ref) (bool, error) { return false, nil },
			store: func(_ context.Context, ref secretref.Ref, value string) error {
				if ref.Raw != "keychain://cloudstic/store/prod-store/password" {
					t.Fatalf("ref=%q", ref.Raw)
				}
				if value != "super-secret" {
					t.Fatalf("value=%q", value)
				}
				return nil
			},
		},
	})

	gotRef, err := secretReference(context.Background(),
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ context.Context, _ string, _ []string) (string, error) {
			return "macOS Keychain (keychain://)", nil
		},
		func(_ context.Context, label, def string) (string, error) { return def, nil },
		func(_ context.Context, _ string) (string, error) { return "super-secret", nil },
		func(string) (string, bool) { return "", false },
		resolver,
	)
	if err != nil {
		t.Fatalf("secretReference: %v", err)
	}
	if gotRef != "keychain://cloudstic/store/prod-store/password" {
		t.Fatalf("ref=%q", gotRef)
	}
}

func TestSecretReference_EnvFallback(t *testing.T) {
	resolver := secretref.NewResolver(nil)
	gotRef, err := secretReference(context.Background(),
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ context.Context, _ string, _ []string) (string, error) {
			return "Environment variable (env://)", nil
		},
		func(_ context.Context, label, def string) (string, error) {
			if label != "Env var name" {
				t.Fatalf("unexpected label: %s", label)
			}
			return def, nil
		},
		func(_ context.Context, _ string) (string, error) {
			t.Fatal("promptSecret should not be called")
			return "", nil
		},
		func(string) (string, bool) { return "", true },
		resolver,
	)
	if err != nil {
		t.Fatalf("secretReference: %v", err)
	}
	if gotRef != "env://CLOUDSTIC_PASSWORD" {
		t.Fatalf("ref=%q", gotRef)
	}
}

func TestSecretReference_KeychainWriteError(t *testing.T) {
	resolver := secretref.NewResolver(map[string]secretref.Backend{
		"keychain": writableBackendStub{
			scheme:      "keychain",
			displayName: "macOS Keychain",
			defaultRef:  "keychain://cloudstic/store/prod-store/password",
			exists:      func(context.Context, secretref.Ref) (bool, error) { return false, nil },
			store:       func(context.Context, secretref.Ref, string) error { return errors.New("write failed") },
		},
	})
	_, err := secretReference(context.Background(),
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ context.Context, _ string, _ []string) (string, error) {
			return "macOS Keychain (keychain://)", nil
		},
		func(_ context.Context, _ string, def string) (string, error) { return def, nil },
		func(_ context.Context, _ string) (string, error) { return "secret", nil },
		func(string) (string, bool) { return "", false },
		resolver,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecretReference_EmptySecret(t *testing.T) {
	resolver := secretref.NewResolver(map[string]secretref.Backend{
		"keychain": writableBackendStub{scheme: "keychain", displayName: "macOS Keychain", defaultRef: "keychain://cloudstic/store/prod-store/password"},
	})
	_, err := secretReference(context.Background(),
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ context.Context, _ string, _ []string) (string, error) {
			return "macOS Keychain (keychain://)", nil
		},
		func(_ context.Context, _ string, def string) (string, error) { return def, nil },
		func(_ context.Context, _ string) (string, error) { return "", nil },
		func(string) (string, bool) { return "", false },
		resolver,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecretReference_DarwinKeychainAdoptsExisting(t *testing.T) {
	resolver := secretref.NewResolver(map[string]secretref.Backend{
		"keychain": writableBackendStub{
			scheme:      "keychain",
			displayName: "macOS Keychain",
			defaultRef:  "keychain://cloudstic/store/prod-store/password",
			exists: func(_ context.Context, ref secretref.Ref) (bool, error) {
				if ref.Raw != "keychain://cloudstic/store/prod-store/password" {
					t.Fatalf("ref=%q", ref.Raw)
				}
				return true, nil
			},
			store: func(context.Context, secretref.Ref, string) error {
				t.Fatal("store should not be called when secret exists")
				return nil
			},
		},
	})
	gotRef, err := secretReference(context.Background(),
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ context.Context, _ string, _ []string) (string, error) {
			return "macOS Keychain (keychain://)", nil
		},
		func(_ context.Context, _ string, def string) (string, error) { return def, nil },
		func(_ context.Context, _ string) (string, error) {
			t.Fatal("promptSecret should not be called when key exists")
			return "", nil
		},
		func(string) (string, bool) { return "", false },
		resolver,
	)
	if err != nil {
		t.Fatalf("secretReference: %v", err)
	}
	if gotRef != "keychain://cloudstic/store/prod-store/password" {
		t.Fatalf("ref=%q", gotRef)
	}
}

func TestSecretReference_DarwinEnvUnsetSwitchesToKeychain(t *testing.T) {
	selectCall := 0
	resolver := secretref.NewResolver(map[string]secretref.Backend{
		"keychain": writableBackendStub{
			scheme:      "keychain",
			displayName: "macOS Keychain",
			defaultRef:  "keychain://cloudstic/store/prod-store/password",
			exists:      func(context.Context, secretref.Ref) (bool, error) { return false, nil },
			store:       func(context.Context, secretref.Ref, string) error { return nil },
		},
	})
	gotRef, err := secretReference(context.Background(),
		"prod-store",
		"repository password",
		"CLOUDSTIC_PASSWORD",
		"password",
		func(_ context.Context, _ string, _ []string) (string, error) {
			selectCall++
			if selectCall == 1 {
				return "Environment variable (env://)", nil
			}
			return "Store in macOS Keychain instead (keychain://)", nil
		},
		func(_ context.Context, label, def string) (string, error) {
			if label != "Env var name" {
				t.Fatalf("unexpected prompt line label: %s", label)
			}
			return "UNSET_PASSWORD", nil
		},
		func(_ context.Context, _ string) (string, error) { return "secret-value", nil },
		func(string) (string, bool) { return "", false },
		resolver,
	)
	if err != nil {
		t.Fatalf("secretReference: %v", err)
	}
	if gotRef != "keychain://cloudstic/store/prod-store/password" {
		t.Fatalf("ref=%q", gotRef)
	}
}

func TestHasExplicitEncryption(t *testing.T) {
	if HasExplicitEncryption(profile.Store{}) {
		t.Fatal("expected false for empty store")
	}
	if !HasExplicitEncryption(profile.Store{PasswordSecret: "env://CLOUDSTIC_PASSWORD"}) {
		t.Fatal("expected true when password secret is set")
	}
}

func TestConfigureSelection_Password(t *testing.T) {
	var out strings.Builder
	s, err := configureSelection(context.Background(),
		profile.Store{},
		"prod",
		"Password (recommended for interactive use)",
		func(context.Context, string, string, string, string) (string, error) {
			return "env://MY_BACKUP_PASSWORD", nil
		},
		func(context.Context, string, string) (string, error) { return "", nil },
		&out,
	)
	if err != nil {
		t.Fatalf("configureSelection: %v", err)
	}
	if s.PasswordSecret != "env://MY_BACKUP_PASSWORD" {
		t.Fatalf("password secret=%q", s.PasswordSecret)
	}
	if !strings.Contains(out.String(), "Encryption: password via env://MY_BACKUP_PASSWORD") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestConfigureSelection_KMS(t *testing.T) {
	var out strings.Builder
	s, err := configureSelection(context.Background(),
		profile.Store{},
		"prod",
		"AWS KMS key (enterprise)",
		func(context.Context, string, string, string, string) (string, error) { return "", nil },
		func(ctx context.Context, label, def string) (string, error) {
			switch label {
			case "KMS key ARN":
				return "arn:aws:kms:us-east-1:123:key/abc", nil
			case "KMS region":
				return "us-east-1", nil
			default:
				return def, nil
			}
		},
		&out,
	)
	if err != nil {
		t.Fatalf("configureSelection: %v", err)
	}
	if s.KMSKeyARN == "" || s.KMSRegion != "us-east-1" {
		t.Fatalf("unexpected kms values: arn=%q region=%q", s.KMSKeyARN, s.KMSRegion)
	}
}

func TestConfigureSelection_NoEncryption(t *testing.T) {
	var out strings.Builder
	_, err := configureSelection(context.Background(),
		profile.Store{},
		"prod",
		"No encryption (not recommended)",
		func(context.Context, string, string, string, string) (string, error) { return "", nil },
		func(context.Context, string, string) (string, error) { return "", nil },
		&out,
	)
	if err != nil {
		t.Fatalf("configureSelection: %v", err)
	}
	if !strings.Contains(out.String(), "Encryption: none") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestConfigureSelection_KMSError(t *testing.T) {
	_, err := configureSelection(context.Background(),
		profile.Store{},
		"prod",
		"AWS KMS key (enterprise)",
		func(context.Context, string, string, string, string) (string, error) { return "", nil },
		func(ctx context.Context, label, def string) (string, error) {
			if label == "KMS key ARN" {
				return "", nil
			}
			return def, nil
		},
		&strings.Builder{},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "KMS key ARN is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// scriptedPrompter answers prompts from a fixed list, which is what replaces
// the fake-stdin plumbing these tests needed while they lived in cmd/cloudstic.
type scriptedPrompter struct {
	selects []string
	lines   []string
	secrets []string
}

func (p *scriptedPrompter) CanPrompt() bool { return true }

func (p *scriptedPrompter) PromptSelect(_ context.Context, _ string, options []string) (string, error) {
	if len(p.selects) == 0 {
		return "", errors.New("no scripted selection left")
	}
	want := p.selects[0]
	p.selects = p.selects[1:]
	for _, o := range options {
		if strings.HasPrefix(o, want) {
			return o, nil
		}
	}
	return "", fmt.Errorf("scripted selection %q matched none of %v", want, options)
}

func (p *scriptedPrompter) PromptLine(_ context.Context, _, def string) (string, error) {
	if len(p.lines) == 0 {
		return def, nil
	}
	v := p.lines[0]
	p.lines = p.lines[1:]
	return v, nil
}

func (p *scriptedPrompter) PromptValidatedLine(ctx context.Context, label, def string, _ func(string) error) (string, error) {
	return p.PromptLine(ctx, label, def)
}

func (p *scriptedPrompter) PromptSecret(context.Context, string) (string, error) {
	if len(p.secrets) == 0 {
		return "", errors.New("no scripted secret left")
	}
	v := p.secrets[0]
	p.secrets = p.secrets[1:]
	return v, nil
}

func (p *scriptedPrompter) PromptConfirm(context.Context, string, bool) (bool, error) {
	return true, nil
}

// TestConfigureEncryption_PasswordViaEnvRef drives the whole flow — select a
// method, name an environment variable, save the profiles file — with no
// terminal. In cmd/cloudstic this test had to install a fake os.Stdin.
func TestConfigureEncryption_PasswordViaEnvRef(t *testing.T) {
	t.Setenv("MY_BACKUP_PASSWORD", "set-for-test")
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	cfg := &profile.Config{
		Version: 1,
		Stores:  map[string]profile.Store{"prod": {URI: "local:/tmp/store"}},
	}

	resolver := secretref.NewResolver(map[string]secretref.Backend{})
	p := &scriptedPrompter{selects: []string{"Password"}, lines: []string{"MY_BACKUP_PASSWORD"}}

	var out, errOut strings.Builder
	ConfigureEncryption(context.Background(), p, cfg, "prod", profilesPath, resolver, &out, &errOut)

	if got := cfg.Stores["prod"].PasswordSecret; got != "env://MY_BACKUP_PASSWORD" {
		t.Fatalf("password secret=%q, want env://MY_BACKUP_PASSWORD", got)
	}
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	if !strings.Contains(string(raw), "password_secret: env://MY_BACKUP_PASSWORD") {
		t.Fatalf("expected the choice to be saved:\n%s", raw)
	}
}

// TestConfigureEncryption_NoEncryptionSavesNothing pins that declining
// encryption leaves the entry untouched rather than writing an empty choice.
func TestConfigureEncryption_NoEncryptionSavesNothing(t *testing.T) {
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	cfg := &profile.Config{
		Version: 1,
		Stores:  map[string]profile.Store{"prod": {URI: "local:/tmp/store"}},
	}
	p := &scriptedPrompter{selects: []string{"No encryption"}}

	var out, errOut strings.Builder
	ConfigureEncryption(context.Background(), p, cfg, "prod", profilesPath, secretref.NewResolver(nil), &out, &errOut)

	if s := cfg.Stores["prod"]; HasExplicitEncryption(s) {
		t.Fatalf("declining encryption must record none, got %+v", s)
	}
	if _, err := os.Stat(profilesPath); err == nil {
		t.Fatal("declining encryption must not write the profiles file")
	}
}
