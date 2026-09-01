package main

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"
)

// TestResolveClientConfig_ProfileOverridesEnvironment documents and locks in
// the precedence #266 asked to make explicit: a selected profile is a named
// choice the user invoked with -profile, so it overrides an ambient
// environment variable the same way it overrides a built-in default. Only an
// explicit CLI flag (TestApplyProfileStore_CLIFlagOverrides) beats it.
func TestResolveClientConfig_ProfileOverridesEnvironment(t *testing.T) {
	t.Setenv("CLOUDSTIC_S3_REGION", "env-region")
	g := newTestGlobalFlags()
	if g.s3Region != "env-region" {
		t.Fatalf("s3Region=%q want env-region (environment should win over default)", g.s3Region)
	}
	if g.origins["s3-region"] != originEnv {
		t.Fatalf("origins[s3-region]=%v want originEnv", g.origins["s3-region"])
	}

	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	err := profile.Save(profilesPath, &profile.Config{
		Version: 1,
		Stores: map[string]profile.Store{
			"s": {URI: "s3:bucket", S3Region: "profile-region"},
		},
		Profiles: map[string]profile.Profile{
			"p": {Source: "local:/data", Store: "s"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}
	g.profile = "p"
	g.profilesFile = profilesPath

	resolved, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if resolved.Store.S3.Region != "profile-region" {
		t.Fatalf("store.s3.region=%q want profile-region (profile should win over environment)", resolved.Store.S3.Region)
	}
}

func TestResolveClientConfig_AppliesProfileStore(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	err := profile.Save(profilesPath, &profile.Config{
		Version: 1,
		Stores: map[string]profile.Store{
			"s": {URI: "s3:bucket/prefix", S3Region: "eu-west-1", S3Profile: "prod"},
		},
		Profiles: map[string]profile.Profile{
			"p": {Source: "local:/data", Store: "s"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	g := newTestGlobalFlags()
	g.profile = "p"
	g.profilesFile = profilesPath

	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if cfg.Store.URI != "s3:bucket/prefix" {
		t.Fatalf("store=%q want s3:bucket/prefix", cfg.Store.URI)
	}
	if cfg.Store.S3.Region != "eu-west-1" {
		t.Fatalf("s3Region=%q want eu-west-1", cfg.Store.S3.Region)
	}
	if cfg.Store.S3.Profile != "prod" {
		t.Fatalf("s3Profile=%q want prod", cfg.Store.S3.Profile)
	}
}

func TestResolveClientConfig_EncryptionFields(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	t.Setenv("TEST_BACKUP_PASSWORD", "s3cret")
	t.Setenv("TEST_ENC_KEY", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("TEST_RECOVERY_KEY", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")

	err := profile.Save(profilesPath, &profile.Config{
		Version: 1,
		Stores: map[string]profile.Store{
			"enc": {
				URI:                 "s3:bucket/enc",
				PasswordSecret:      "env://TEST_BACKUP_PASSWORD",
				EncryptionKeySecret: "env://TEST_ENC_KEY",
				RecoveryKeySecret:   "env://TEST_RECOVERY_KEY",
				KMSKeyARN:           "arn:aws:kms:us-east-1:123456:key/abcd",
				KMSRegion:           "us-east-1",
				KMSEndpoint:         "https://kms.example.com",
			},
		},
		Profiles: map[string]profile.Profile{
			"p": {Source: "local:/data", Store: "enc"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	g := newTestGlobalFlags()
	g.profile = "p"
	g.profilesFile = profilesPath

	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if cfg.Unlock.Password != "s3cret" {
		t.Fatalf("password=%q want s3cret", cfg.Unlock.Password)
	}
	if cfg.Unlock.EncryptionKey != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("encryptionKey=%q want aaa...", cfg.Unlock.EncryptionKey)
	}
	if cfg.Unlock.RecoveryKey == "" {
		t.Fatal("expected recoveryKey to be set from env")
	}
	if cfg.Unlock.KMS.KeyARN != "arn:aws:kms:us-east-1:123456:key/abcd" {
		t.Fatalf("kmsKeyARN=%q want arn:...", cfg.Unlock.KMS.KeyARN)
	}
	if cfg.Unlock.KMS.Region != "us-east-1" {
		t.Fatalf("kmsRegion=%q want us-east-1", cfg.Unlock.KMS.Region)
	}
	if cfg.Unlock.KMS.Endpoint != "https://kms.example.com" {
		t.Fatalf("kmsEndpoint=%q want https://kms.example.com", cfg.Unlock.KMS.Endpoint)
	}
}

func TestResolveClientConfig_DoesNotOverrideExplicitStoreFlag(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	err := profile.Save(profilesPath, &profile.Config{
		Version: 1,
		Stores: map[string]profile.Store{
			"s": {URI: "s3:bucket/prefix"},
		},
		Profiles: map[string]profile.Profile{
			"p": {Source: "local:/data", Store: "s"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	g := newTestGlobalFlags("-store", "local:/explicit")
	g.profile = "p"
	g.profilesFile = profilesPath
	g.store = "local:/explicit"

	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if cfg.Store.URI != "local:/explicit" {
		t.Fatalf("store=%q want local:/explicit", cfg.Store.URI)
	}
	// Resolution must not write back into the parsed flags.
	if g.store != "local:/explicit" {
		t.Fatalf("parsed flags mutated: store=%q", g.store)
	}
}

// globalFlags holds parsed command-line input. It is deliberately not a
// factory: store, client, keychain, and KMS construction live in
// storebuild.go/keychain.go and take resolved configuration values, so they can
// be tested without flag parsing. Only accessors over the parsed values belong
// here.
func TestGlobalFlagsHasNoConstructionMethods(t *testing.T) {
	allowed := map[string]bool{
		"jsonEnabled":  true,
		"flagProvided": true,
	}

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	fset := token.NewFileSet()
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := star.X.(*ast.Ident)
			if !ok || ident.Name != "globalFlags" {
				continue
			}
			if !allowed[fn.Name.Name] {
				t.Errorf("%s: globalFlags must not have method %q; make it a function taking a resolved config value",
					path, fn.Name.Name)
			}
		}
	}
}

// TestClientConfigFromFlags_LeavesS3RegionToConstruction is the flag-path half
// of TestClientConfigFromProfileStore_DefaultRegion (cmd_store_test.go).
//
// The S3 region default belongs to pkg/open, which applies it once at
// construction so a configuration built from a profile and one built from
// flags cannot disagree. The profile path was moved to that rule; the flag
// path was left behind, pre-filling "us-east-1" as the flag's default, so
// open's default never fired here and the two definitions agreed only because
// the literal was duplicated in both packages.
func TestClientConfigFromFlags_LeavesS3RegionToConstruction(t *testing.T) {
	cfg := clientConfigFromFlags(newTestGlobalFlags())
	if cfg.Store.S3.Region != "" {
		t.Fatalf("flags with no -s3-region must leave the region unset for pkg/open to fill, got %q", cfg.Store.S3.Region)
	}
}

// TestClientConfigFromFlags_ExplicitS3RegionSurvives guards the other half: not
// pre-filling a default must not lose a region the user actually passed.
func TestClientConfigFromFlags_ExplicitS3RegionSurvives(t *testing.T) {
	g := newTestGlobalFlags("-s3-region", "eu-west-3")
	g.s3Region = "eu-west-3"
	if got := clientConfigFromFlags(g).Store.S3.Region; got != "eu-west-3" {
		t.Fatalf("explicit -s3-region must survive into the config, got %q", got)
	}
}

// The object cache reaches the client the way every other knob does: a flag
// with an environment binding, resolved here into a pkg/config.Client that
// pkg/open turns into a cloudstic.WithObjectCache. The root package reads no
// environment variable of its own, so this is the only place the wiring can be
// checked.
func TestResolveClientConfig_ObjectCacheFromEnvironment(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("CLOUDSTIC_OBJECT_CACHE_DIR", cacheDir)
	t.Setenv("CLOUDSTIC_OBJECT_CACHE_BYTES", "1048576")

	g := newTestGlobalFlags()
	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if cfg.ObjectCacheDir != cacheDir {
		t.Errorf("ObjectCacheDir=%q want %q", cfg.ObjectCacheDir, cacheDir)
	}
	if cfg.ObjectCacheBytes != 1<<20 {
		t.Errorf("ObjectCacheBytes=%d want %d", cfg.ObjectCacheBytes, 1<<20)
	}
	if cfg.DisableObjectCache {
		t.Error("DisableObjectCache set without -no-object-cache")
	}
}

// -no-object-cache exists so an explicit "off" can beat a directory inherited
// from the environment. Without it the only way to disable the cache would be
// to unset a variable the user may not control.
func TestResolveClientConfig_NoObjectCacheOverridesAnInheritedDirectory(t *testing.T) {
	t.Setenv("CLOUDSTIC_OBJECT_CACHE_DIR", t.TempDir())

	g := newTestGlobalFlags()
	g.noObjectCache = true
	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if !cfg.DisableObjectCache {
		t.Fatal("DisableObjectCache not set despite -no-object-cache")
	}
	// The directory still resolves; it is open's job to ignore it. Asserting
	// that here keeps the two halves of the decision visible in one place.
	if cfg.ObjectCacheDir == "" {
		t.Error("the inherited directory was cleared rather than overridden")
	}
}

// A malformed budget fails the command rather than being silently ignored.
// Every other environment-bound flag behaves this way, and a typo in a limit
// is the case where quietly carrying on is worst: the user believes they have
// set a bound and has not.
func TestGlobalFlags_MalformedObjectCacheBytesIsRejected(t *testing.T) {
	t.Setenv("CLOUDSTIC_OBJECT_CACHE_BYTES", "512MB")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	g := &globalFlags{}
	specs := repoFlagSpecs(g)
	for _, s := range specs {
		s.bind(fs, s.name, s.bindUsage())
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := applyEnvDefaults(fs, specs); err == nil {
		t.Fatal("a malformed budget was accepted")
	} else if !strings.Contains(err.Error(), "CLOUDSTIC_OBJECT_CACHE_BYTES") {
		t.Errorf("error does not name the variable: %v", err)
	}
}

// The cache is on by default, and the default directory is the platform's
// cache location rather than the config directory. The distinction matters:
// configuration is precious and may be backed up, this is disposable, and on
// macOS the OS may purge it — which is right for a cache and wrong for a
// profiles file.
func TestResolveClientConfig_ObjectCacheDefaultsOnUnderTheOSCacheDir(t *testing.T) {
	// Asserted against os.UserCacheDir itself rather than against the variables
	// that feed it. Those differ per platform — XDG_CACHE_HOME on Linux,
	// $HOME/Library/Caches on macOS, %LocalAppData% on Windows — so naming any
	// of them here would fail on the platforms it does not describe, for a
	// default that is correct.
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no OS cache directory on this platform")
	}

	want, err := defaultObjectCachePath()
	if err != nil {
		t.Fatalf("defaultObjectCachePath: %v", err)
	}
	if got := filepath.Join(base, "cloudstic", "objects"); want != got {
		t.Fatalf("default = %q, want %q", want, got)
	}

	g := newTestGlobalFlags()
	cfg, err := resolveClientConfig(g)
	if err != nil {
		t.Fatalf("resolveClientConfig: %v", err)
	}
	if cfg.ObjectCacheDir != want {
		t.Errorf("ObjectCacheDir=%q want %q (the cache should be on by default)", cfg.ObjectCacheDir, want)
	}
	if cfg.DisableObjectCache {
		t.Error("the cache is disabled by default")
	}
}

// Resolving the default must not create the directory. Help and shell
// completion resolve flags too, and creating a directory as a side effect of
// asking for help is the bug -profiles-file's late default exists to avoid.
func TestDefaultObjectCachePath_CreatesNothing(t *testing.T) {
	// Redirected into a temp directory so the assertion is about this test's
	// path and not about whether the developer has ever run the CLI. Windows
	// derives the cache directory from %LocalAppData% and will not follow
	// these, so the test says so rather than asserting against a real one.
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)
	base, err := os.UserCacheDir()
	if err != nil || !strings.HasPrefix(base, tmp) {
		t.Skip("os.UserCacheDir does not follow the environment on this platform")
	}

	path, err := defaultObjectCachePath()
	if err != nil {
		t.Fatalf("defaultObjectCachePath: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("resolving the default created %s (stat err = %v)", path, err)
	}
}
