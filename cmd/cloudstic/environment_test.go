package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func clearCommandEnvironment(t *testing.T) {
	t.Helper()
	for _, variable := range environmentVariables {
		t.Setenv(variable.Name, "")
	}
}

func TestEnvironment_InventoryFitsProvenanceStorage(t *testing.T) {
	seen := map[string]bool{}
	for _, variable := range environmentVariables {
		if variable.Flag != "" {
			seen[variable.Flag] = true
		}
	}
	if len(seen) > maxEnvironmentDestinations {
		t.Fatalf("environment inventory has %d flag destinations; maximum is %d", len(seen), maxEnvironmentDestinations)
	}
}

func TestEnvironment_SecretValuesNeverAppearInHelp(t *testing.T) {
	clearCommandEnvironment(t)
	const secret = "sentinel-secret-that-must-not-appear"
	for _, variable := range environmentVariables {
		if variable.Sensitive {
			t.Setenv(variable.Name, secret)
		}
	}
	fs := flag.NewFlagSet("secrets", flag.ContinueOnError)
	var output bytes.Buffer
	fs.SetOutput(&output)
	seen := map[string]bool{}
	for _, variable := range environmentVariables {
		if variable.Flag == "" || seen[variable.Flag] {
			continue
		}
		seen[variable.Flag] = true
		fs.String(variable.Flag, "built-in", variable.Description)
	}
	err := parseFlags(fs, []string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags(-h) error = %v, want flag.ErrHelp", err)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("help exposed a secret environment value:\n%s", output.String())
	}
	for _, variable := range environmentVariables {
		if variable.Sensitive && variable.Flag != "" && !strings.Contains(output.String(), variable.Name) {
			t.Errorf("help does not name supported environment variable %s", variable.Name)
		}
	}
}

func TestEnvironment_GlobalHelpKeepsBuiltInDefaults(t *testing.T) {
	clearCommandEnvironment(t)
	t.Setenv("CLOUDSTIC_PASSWORD", "live-password")
	t.Setenv("CLOUDSTIC_STORE", "s3:private-bucket")
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	var output bytes.Buffer
	fs.SetOutput(&output)
	addGlobalFlags(fs)
	err := parseFlags(fs, []string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags(-h) error = %v, want flag.ErrHelp", err)
	}
	if strings.Contains(output.String(), "live-password") || strings.Contains(output.String(), "s3:private-bucket") {
		t.Fatalf("global help exposed live environment values:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "local:./backup_store") || !strings.Contains(output.String(), "CLOUDSTIC_PASSWORD") {
		t.Fatalf("global help does not show built-in defaults and environment names:\n%s", output.String())
	}
}

func TestEnvironment_BooleanParsing(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{"1", true}, {"t", true}, {"TRUE", true}, {"True", true}, {"true", true},
		{"0", false}, {"f", false}, {"FALSE", false}, {"False", false}, {"false", false},
	} {
		t.Run(test.value, func(t *testing.T) {
			clearCommandEnvironment(t)
			t.Setenv("CLOUDSTIC_DISABLE_PACKFILE", test.value)
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			g := addGlobalFlags(fs)
			if err := parseFlags(fs, nil); err != nil {
				t.Fatalf("parseFlags() error = %v", err)
			}
			if g.disablePackfile != test.want {
				t.Fatalf("disablePackfile = %v, want %v", g.disablePackfile, test.want)
			}
		})
	}
}

func TestEnvironment_InvalidBooleanIsActionable(t *testing.T) {
	clearCommandEnvironment(t)
	t.Setenv("CLOUDSTIC_DISABLE_PACKFILE", "enabled")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	addGlobalFlags(fs)
	err := parseFlags(fs, nil)
	if err == nil || !strings.Contains(err.Error(), "CLOUDSTIC_DISABLE_PACKFILE") || !strings.Contains(err.Error(), "invalid boolean") {
		t.Fatalf("parseFlags() error = %v, want actionable environment-variable error", err)
	}
}

func TestEnvironment_PrecedenceAndProvenance(t *testing.T) {
	clearCommandEnvironment(t)
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {URI: "s3:profile-bucket", S3Region: "profile-region"},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"daily": {Source: "local:/data", Store: "remote"},
		},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}
	t.Setenv("CLOUDSTIC_S3_REGION", "environment-region")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, []string{"-profile", "daily", "-profiles-file", profilesPath}); err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	if err := g.applyProfileStoreOverrides(); err != nil {
		t.Fatalf("applyProfileStoreOverrides() error = %v", err)
	}
	if g.s3Region != "environment-region" || g.valueSource("s3-region") != valueSourceEnvironment {
		t.Fatalf("environment precedence: region=%q source=%q", g.s3Region, g.valueSource("s3-region"))
	}
	if g.store != "s3:profile-bucket" || g.valueSource("store") != valueSourceProfile {
		t.Fatalf("profile provenance: store=%q source=%q", g.store, g.valueSource("store"))
	}
	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	g = addGlobalFlags(fs)
	if err := parseFlags(fs, []string{"-s3-region", "flag-region"}); err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	if g.s3Region != "flag-region" || g.valueSource("s3-region") != valueSourceFlag {
		t.Fatalf("flag precedence: region=%q source=%q", g.s3Region, g.valueSource("s3-region"))
	}
	if g.valueSource("store") != valueSourceDefault {
		t.Fatalf("default provenance = %q", g.valueSource("store"))
	}
}

func TestEnvironment_BackupSourceOverridesProfile(t *testing.T) {
	clearCommandEnvironment(t)
	t.Setenv("CLOUDSTIC_SOURCE", "local:/environment")
	base, err := parseBackupArgs([]string{"-profile", "daily"})
	if err != nil {
		t.Fatalf("parseBackupArgs() error = %v", err)
	}
	cfg := &cloudstic.ProfilesConfig{
		Stores: map[string]cloudstic.ProfileStore{},
		Profiles: map[string]cloudstic.BackupProfile{
			"daily": {Source: "local:/profile"},
		},
	}
	effective, err := mergeProfileBackupArgs(base, "daily", cfg.Profiles["daily"], cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs() error = %v", err)
	}
	if effective.sourceURI != "local:/environment" || effective.valueSource("source") != valueSourceEnvironment {
		t.Fatalf("source = %q (%s), want environment value", effective.sourceURI, effective.valueSource("source"))
	}

	t.Setenv("CLOUDSTIC_SOURCE", "")
	base, err = parseBackupArgs([]string{"-profile", "daily"})
	if err != nil {
		t.Fatalf("parseBackupArgs() error = %v", err)
	}
	effective, err = mergeProfileBackupArgs(base, "daily", cfg.Profiles["daily"], cfg)
	if err != nil {
		t.Fatalf("mergeProfileBackupArgs() error = %v", err)
	}
	if effective.sourceURI != "local:/profile" || effective.valueSource("source") != valueSourceProfile {
		t.Fatalf("source = %q (%s), want profile value", effective.sourceURI, effective.valueSource("source"))
	}
}

func TestB2Credentials_ProfileSecretReferences(t *testing.T) {
	clearCommandEnvironment(t)
	t.Setenv("PROFILE_B2_KEY_ID", "profile-key-id")
	t.Setenv("PROFILE_B2_APP_KEY", "profile-app-key")
	g, err := globalFlagsFromProfileStore(cloudstic.ProfileStore{
		URI:            "b2:bucket",
		B2KeyIDSecret:  "env://PROFILE_B2_KEY_ID",
		B2AppKeySecret: "env://PROFILE_B2_APP_KEY",
	})
	if err != nil {
		t.Fatalf("globalFlagsFromProfileStore() error = %v", err)
	}
	if g.b2KeyID != "profile-key-id" || g.b2AppKey != "profile-app-key" {
		t.Fatalf("B2 credentials = (%q, %q)", g.b2KeyID, g.b2AppKey)
	}
}

func TestB2Credentials_EnvironmentAndFlagPrecedence(t *testing.T) {
	clearCommandEnvironment(t)
	t.Setenv("B2_KEY_ID", "environment-key-id")
	t.Setenv("B2_APP_KEY", "environment-app-key")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, []string{"-b2-key-id", "flag-key-id"}); err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	if g.b2KeyID != "flag-key-id" || g.valueSource("b2-key-id") != valueSourceFlag {
		t.Fatalf("B2 key ID = %q (%s)", g.b2KeyID, g.valueSource("b2-key-id"))
	}
	if g.b2AppKey != "environment-app-key" || g.valueSource("b2-app-key") != valueSourceEnvironment {
		t.Fatalf("B2 app key = %q (%s)", g.b2AppKey, g.valueSource("b2-app-key"))
	}
}

func TestB2Credentials_StoreNewPersistsFlagsAndSecretReferences(t *testing.T) {
	clearCommandEnvironment(t)
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	r := newRunner([]string{"new", "-no-prompt", "-profiles-file", profilesPath, "-name", "archive", "-uri", "b2:bucket/prefix", "-b2-key-id", "inline-key-id", "-b2-app-key-secret", "env://B2_APP_KEY"})
	r.out = &bytes.Buffer{}
	r.errOut = &bytes.Buffer{}
	if code := runStore(r, context.Background()); code != 0 {
		t.Fatalf("runStore() code = %d, stderr = %s", code, r.errOut)
	}
	cfg, err := cloudstic.LoadProfilesFile(profilesPath)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	store := cfg.Stores["archive"]
	if store.B2KeyID != "inline-key-id" || store.B2AppKeySecret != "env://B2_APP_KEY" {
		t.Fatalf("stored B2 credentials = %+v", store)
	}
}

func TestEnvironmentDocumentationMatchesInventory(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "docs", "user-guide.md"))
	if err != nil {
		t.Fatalf("read user guide: %v", err)
	}
	want := environmentDocumentation() + "\n"
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for name, contents := range map[string][]byte{
		"lf":   []byte(normalized),
		"crlf": []byte(strings.ReplaceAll(normalized, "\n", "\r\n")),
	} {
		got, ok := generatedEnvironmentSection(contents)
		if !ok {
			t.Fatalf("%s user guide is missing generated environment-variable markers", name)
		}
		if got != want {
			t.Fatalf("%s environment-variable documentation is stale; regenerate it from environmentVariables\n--- want ---\n%s--- got ---\n%s", name, want, got)
		}
	}
}

func generatedEnvironmentSection(raw []byte) (string, bool) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	const begin = "<!-- BEGIN GENERATED ENVIRONMENT VARIABLES -->\n"
	const end = "<!-- END GENERATED ENVIRONMENT VARIABLES -->"
	start := strings.Index(text, begin)
	finish := strings.Index(text, end)
	if start < 0 || finish < 0 || finish < start {
		return "", false
	}
	return text[start+len(begin) : finish], true
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	var candidates []string
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if _, filename, _, ok := runtime.Caller(0); ok && filepath.IsAbs(filename) {
		candidates = append(candidates, filepath.Dir(filename))
	}
	for _, candidate := range candidates {
		for dir := candidate; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	t.Fatal("locate repository root")
	return ""
}
