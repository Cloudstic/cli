package main

import (
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"
)

// The built-in OAuth client is injected via ldflags at release time, so a plain
// `go build` has an empty client ID and any authorization it starts is rejected
// for carrying no client_id. GOOGLE_APPLICATION_CREDENTIALS is the documented
// way out (internal/sourceoauth/defaults.go), but until these flags existed
// nothing on the `auth login` path consulted it — the variable was bound only to
// `backup`'s flag of the same name.
func TestApplyAuthLoginCredentialFlags(t *testing.T) {
	entry := profile.Auth{
		Provider:    config.ProviderGoogle,
		GoogleCreds: "/from-entry.json",
	}

	t.Run("environment fills an entry that names nothing", func(t *testing.T) {
		srcCfg, err := config.SourceForAuth(profile.Auth{Provider: config.ProviderGoogle})
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		// An environment value lands in the flag variable without being marked
		// as explicitly provided, which is what applyEnvDefaults does.
		a := &authLoginArgs{globalFlags: newTestGlobalFlags(), googleCreds: "/from-env.json"}

		applyAuthLoginCredentialFlags(&srcCfg, a)

		if srcCfg.Google.CredsPath != "/from-env.json" {
			t.Errorf("creds = %q, want the environment value: an entry naming no "+
				"credentials leaves a locally built binary with no OAuth client at all",
				srcCfg.Google.CredsPath)
		}
	})

	// The documented precedence is flag > entry > environment. An environment
	// value must not silently redirect an entry that names something else.
	t.Run("entry beats the environment", func(t *testing.T) {
		srcCfg, err := config.SourceForAuth(entry)
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		a := &authLoginArgs{globalFlags: newTestGlobalFlags(), googleCreds: "/from-env.json"}

		applyAuthLoginCredentialFlags(&srcCfg, a)

		if srcCfg.Google.CredsPath != "/from-entry.json" {
			t.Errorf("creds = %q, want the entry's value", srcCfg.Google.CredsPath)
		}
	})

	t.Run("an explicit flag beats the entry", func(t *testing.T) {
		srcCfg, err := config.SourceForAuth(entry)
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		a := &authLoginArgs{
			globalFlags: testProvided(newTestGlobalFlags(), "google-credentials"),
			googleCreds: "/from-flag.json",
		}

		applyAuthLoginCredentialFlags(&srcCfg, a)

		if srcCfg.Google.CredsPath != "/from-flag.json" {
			t.Errorf("creds = %q, want the explicitly passed flag", srcCfg.Google.CredsPath)
		}
	})

	t.Run("every credential input is folded", func(t *testing.T) {
		srcCfg, err := config.SourceForAuth(profile.Auth{Provider: config.ProviderGoogle})
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		a := &authLoginArgs{
			globalFlags:     newTestGlobalFlags(),
			googleCreds:     "/creds.json",
			googleCredsRef:  "keychain://creds",
			googleCredsJSON: `{"x":1}`,
		}

		applyAuthLoginCredentialFlags(&srcCfg, a)

		if srcCfg.Google.CredsPath != "/creds.json" ||
			srcCfg.Google.CredsRef != "keychain://creds" ||
			srcCfg.Google.CredsJSON != `{"x":1}` {
			t.Errorf("Google = %+v, want every credential input carried over", srcCfg.Google)
		}
	})

	t.Run("onedrive client id", func(t *testing.T) {
		srcCfg, err := config.SourceForAuth(profile.Auth{Provider: config.ProviderOneDrive})
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		a := &authLoginArgs{globalFlags: newTestGlobalFlags(), onedriveClientID: "client-from-env"}

		applyAuthLoginCredentialFlags(&srcCfg, a)

		if srcCfg.OneDrive.ClientID != "client-from-env" {
			t.Errorf("client ID = %q, want the environment value", srcCfg.OneDrive.ClientID)
		}
	})

	// Nothing configured anywhere must leave the config untouched rather than
	// writing empty strings over the entry's values.
	t.Run("no inputs leaves the entry alone", func(t *testing.T) {
		srcCfg, err := config.SourceForAuth(entry)
		if err != nil {
			t.Fatalf("SourceForAuth: %v", err)
		}
		a := &authLoginArgs{globalFlags: newTestGlobalFlags()}

		applyAuthLoginCredentialFlags(&srcCfg, a)

		if srcCfg.Google.CredsPath != "/from-entry.json" {
			t.Errorf("creds = %q, want the entry's value untouched", srcCfg.Google.CredsPath)
		}
	})
}

// TestAuthLoginBindsTheDocumentedEnvironmentVariables pins the bindings this fix
// exists for. Losing one would silently restore the original bug: a locally
// built binary starting an authorization with no client_id.
func TestAuthLoginBindsTheDocumentedEnvironmentVariables(t *testing.T) {
	_, input := declareAuthLoginArgs(newTestGlobalFlags())

	bound := map[string]string{}
	for _, f := range input.flags {
		if f.env != "" {
			bound[f.name] = f.env
		}
	}

	want := map[string]string{
		"google-credentials":      "GOOGLE_APPLICATION_CREDENTIALS",
		"google-credentials-json": "GOOGLE_CREDENTIALS_JSON",
		"onedrive-client-id":      "ONEDRIVE_CLIENT_ID",
	}
	for flag, env := range want {
		if bound[flag] != env {
			t.Errorf("flag -%s binds %q, want %q", flag, bound[flag], env)
		}
	}
}
