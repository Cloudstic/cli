package open

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/profile"
	"github.com/cloudstic/cli/pkg/secretref"
	"github.com/cloudstic/cli/pkg/secretref/backends"
)

// FromProfile is the whole point of pkg/open for a library caller: a profiles
// file and a name, with none of the wiring that used to live in package main
// (RFC 0022 §7). These tests go through the real profile.Load, so they also
// pin that the profiles file the CLI writes is the one this reads.

// writeProfiles writes a profiles file and returns its path.
func writeProfiles(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), profile.DefaultFilename)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	return path
}

func TestFromProfile_OpensAnUnencryptedLocalRepo(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	path := writeProfiles(t, `
version: 1
stores:
  scratch:
    uri: local:`+repo+`
profiles:
  docs:
    source: local:/does-not-matter
    store: scratch
`)

	client, err := FromProfile(ctx, path, "docs")
	if err != nil {
		t.Fatalf("FromProfile: %v", err)
	}
	if client == nil {
		t.Fatal("FromProfile returned a nil client and no error")
	}
}

func TestFromProfile_Errors(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	path := writeProfiles(t, `
version: 1
stores:
  scratch:
    uri: local:`+repo+`
profiles:
  docs:
    source: local:/docs
    store: scratch
  storeless:
    source: local:/docs
`)

	cases := []struct {
		name    string
		file    string
		profile string
		want    string
	}{
		{"unknown profile", path, "nope", "nope"},
		// A nil store is a legitimate answer from Config.StoreFor, but a
		// client cannot be opened without one, so it becomes an error here.
		{"profile names no store", path, "storeless", "names no store"},
		{"missing profiles file", filepath.Join(t.TempDir(), "absent.yaml"), "docs", "absent.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FromProfile(ctx, tc.file, tc.profile)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A profile may name its credentials as secret references rather than inline.
// Resolving them is what WithSecretResolver overrides, and the resolved value
// has to actually arrive — a reference that is read and then dropped would
// look identical until the repository refused to open.
//
// The repository is a real password-protected one on a local store, so the
// assertion is the strongest available and still dials nothing: NewClient
// resolves the key eagerly, so it opens only if the password reached the
// keychain.
func TestFromProfile_WithSecretResolver(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()

	const password = "correct horse battery staple"
	raw, err := Store(ctx, config.Store{URI: "local:" + repo})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := cloudstic.InitRepo(ctx, raw, cloudstic.WithInitCredentials(
		keychain.Chain{keychain.WithPassword(password)},
	)); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	path := writeProfiles(t, `
version: 1
stores:
  scratch:
    uri: local:`+repo+`
    password_secret: env://TEST_REPO_PASSWORD
profiles:
  docs:
    source: local:/docs
    store: scratch
`)

	// Without a resolver that knows env://, the reference cannot be read. The
	// failure names the profile field, so it says which entry to go fix rather
	// than surfacing later as an unexplained failure to unlock.
	_, err = FromProfile(ctx, path, "docs", WithSecretResolver(secretref.NewResolver(nil)))
	if err == nil {
		t.Fatal("expected an error when no backend can resolve the reference")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error %q does not name the profile field at fault", err)
	}

	// With one, the resolved password opens the repository.
	t.Setenv("TEST_REPO_PASSWORD", password)
	if _, err := FromProfile(ctx, path, "docs", WithSecretResolver(backends.NewDefaultResolver())); err != nil {
		t.Fatalf("FromProfile with a resolvable password reference: %v", err)
	}

	// And the wrong one does not, which is what makes the line above evidence
	// that the reference was read rather than that the repository is open to
	// anyone.
	t.Setenv("TEST_REPO_PASSWORD", "not the password")
	if _, err := FromProfile(ctx, path, "docs", WithSecretResolver(backends.NewDefaultResolver())); err == nil {
		t.Fatal("expected an error from a resolved but incorrect password")
	}
}

// TestFromProfile_WithDecidedOverridesTheProfile is the case FromProfile could
// not serve before WithDecided existed: a caller that has a configuration
// mechanism of its own — flags, its own file, a form — and wants the profile
// only for what that mechanism does not say.
//
// The repository lives where the caller says, not where the profile says, and
// the profile still supplies the password the caller never mentioned.
func TestFromProfile_WithDecidedOverridesTheProfile(t *testing.T) {
	ctx := context.Background()
	callersRepo := t.TempDir()

	const password = "opened by the profile's credential"
	raw, err := Store(ctx, config.Store{URI: "local:" + callersRepo})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := cloudstic.InitRepo(ctx, raw, cloudstic.WithInitCredentials(
		keychain.Chain{keychain.WithPassword(password)},
	)); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	// The profile names a store URI that cannot be constructed at all. Nothing
	// about the environment can make that succeed, so it is an unambiguous
	// marker for "the profile's URI was used".
	path := writeProfiles(t, `
version: 1
stores:
  scratch:
    uri: nope:not-a-real-scheme
    password_secret: env://TEST_DECIDED_PASSWORD
profiles:
  docs:
    source: local:/docs
    store: scratch
`)
	t.Setenv("TEST_DECIDED_PASSWORD", password)

	mine := config.Client{Store: config.Store{URI: "local:" + callersRepo}}
	if _, err := FromProfile(ctx, path, "docs",
		WithDecided(mine, config.FieldsSetIn(mine))); err != nil {
		t.Fatalf("FromProfile with a decided store URI: %v", err)
	}

	// The same call without the override reaches the profile's unusable URI —
	// which is what makes the success above evidence that the caller's URI won,
	// rather than evidence that both happened to work.
	_, err = FromProfile(ctx, path, "docs")
	if err == nil {
		t.Fatal("expected an error from the profile's own store URI")
	}
	if !strings.Contains(err.Error(), `unknown store scheme "nope"`) {
		t.Errorf("error %q is not the profile URI failing; the discriminator no longer discriminates", err)
	}
}

// A decided field's secret reference is never read, so a broken reference on a
// field the caller is replacing must not fail the open. This is the property a
// post-hoc override hook could not have preserved.
func TestFromProfile_DecidedFieldsAreNeverResolved(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()

	const password = "chosen by the caller"
	raw, err := Store(ctx, config.Store{URI: "local:" + repo})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := cloudstic.InitRepo(ctx, raw, cloudstic.WithInitCredentials(
		keychain.Chain{keychain.WithPassword(password)},
	)); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	path := writeProfiles(t, `
version: 1
stores:
  scratch:
    uri: local:`+repo+`
    password_secret: env://CLOUDSTIC_TEST_DEFINITELY_UNSET
profiles:
  docs:
    source: local:/docs
    store: scratch
`)

	mine := config.Client{Unlock: config.Unlock{Password: password}}
	if _, err := FromProfile(ctx, path, "docs",
		WithDecided(mine, config.NewFieldSet(config.FieldPassword))); err != nil {
		t.Fatalf("a decided field's reference must not be resolved, so its being "+
			"broken must not fail the open: %v", err)
	}
}

// TestFromProfile_DefaultsToTheConfigDirectory pins that an empty path lands on
// the same file the CLI uses, which is the reason DefaultPath is exported.
func TestFromProfile_DefaultsToTheConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", dir)
	repo := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, profile.DefaultFilename), []byte(`
version: 1
stores:
  scratch:
    uri: local:`+repo+`
profiles:
  docs:
    source: local:/docs
    store: scratch
`), 0o600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}

	if _, err := FromProfile(context.Background(), "", "docs"); err != nil {
		t.Fatalf("FromProfile with an empty path: %v", err)
	}
}
