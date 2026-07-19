package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func clearCommandEnvironment(t *testing.T) {
	t.Helper()
	for _, variable := range environmentVariables() {
		t.Setenv(variable.Name, "")
	}
}

func TestEnvironmentBindingsAreConsistentAcrossFlagSpecs(t *testing.T) {
	type owner struct {
		flag       string
		sensitive  bool
		documented bool
	}
	owners := make(map[string]owner)
	visit := func(spec flagSpec) {
		for _, environment := range spec.environment {
			if environment.name == "" {
				t.Errorf("flag -%s has an empty environment name", spec.name)
				continue
			}
			current := owner{spec.name, environment.sensitive, environment.documented}
			if previous, ok := owners[environment.name]; ok && previous != current {
				t.Errorf("environment %s has inconsistent owners: %+v and %+v", environment.name, previous, current)
			}
			owners[environment.name] = current
		}
	}
	for _, spec := range globalFlagSpecs {
		visit(spec)
	}
	for _, command := range publicLeafCommands() {
		for _, spec := range command.flags {
			visit(spec)
		}
	}
	for _, standalone := range standaloneEnvironmentVariables {
		name := standalone.Name
		if name == "" {
			t.Error("standalone environment has an empty name")
		} else if previous, ok := owners[name]; ok {
			t.Errorf("standalone environment %s is already owned by flag -%s", name, previous.flag)
		}
	}
}

func TestFormerEnvironmentDefaultsAreOwnedByActiveSpecs(t *testing.T) {
	global := map[string][]string{
		"store":                   {"CLOUDSTIC_STORE"},
		"profile":                 {"CLOUDSTIC_PROFILE"},
		"profiles-file":           {"CLOUDSTIC_PROFILES_FILE"},
		"s3-endpoint":             {"CLOUDSTIC_S3_ENDPOINT"},
		"s3-region":               {"CLOUDSTIC_S3_REGION"},
		"s3-profile":              {"CLOUDSTIC_S3_PROFILE", "AWS_PROFILE"},
		"s3-access-key":           {"AWS_ACCESS_KEY_ID"},
		"s3-secret-key":           {"AWS_SECRET_ACCESS_KEY"},
		"source-sftp-password":    {"CLOUDSTIC_SOURCE_SFTP_PASSWORD"},
		"source-sftp-key":         {"CLOUDSTIC_SOURCE_SFTP_KEY"},
		"source-sftp-insecure":    {"CLOUDSTIC_SOURCE_SFTP_INSECURE"},
		"source-sftp-known-hosts": {"CLOUDSTIC_SOURCE_SFTP_KNOWN_HOSTS"},
		"store-sftp-password":     {"CLOUDSTIC_STORE_SFTP_PASSWORD"},
		"store-sftp-key":          {"CLOUDSTIC_STORE_SFTP_KEY"},
		"store-sftp-insecure":     {"CLOUDSTIC_STORE_SFTP_INSECURE"},
		"store-sftp-known-hosts":  {"CLOUDSTIC_STORE_SFTP_KNOWN_HOSTS"},
		"encryption-key":          {"CLOUDSTIC_ENCRYPTION_KEY"},
		"password":                {"CLOUDSTIC_PASSWORD"},
		"recovery-key":            {"CLOUDSTIC_RECOVERY_KEY"},
		"kms-key-arn":             {"CLOUDSTIC_KMS_KEY_ARN"},
		"kms-region":              {"CLOUDSTIC_KMS_REGION"},
		"kms-endpoint":            {"CLOUDSTIC_KMS_ENDPOINT"},
		"disable-packfile":        {"CLOUDSTIC_DISABLE_PACKFILE"},
	}
	for flagName, environmentNames := range global {
		assertEnvironmentNames(t, "global", globalFlagByName(flagName), environmentNames...)
	}

	backup := backupCommandSpec()
	for flagName, environmentName := range map[string]string{
		"source":                  "CLOUDSTIC_SOURCE",
		"volume-uuid":             "CLOUDSTIC_VOLUME_UUID",
		"google-credentials":      "GOOGLE_APPLICATION_CREDENTIALS",
		"google-credentials-json": "GOOGLE_CREDENTIALS_JSON",
		"google-token-file":       "GOOGLE_TOKEN_FILE",
		"onedrive-client-id":      "ONEDRIVE_CLIENT_ID",
		"onedrive-token-file":     "ONEDRIVE_TOKEN_FILE",
	} {
		spec, ok := backup.flag(flagName)
		if !ok {
			t.Fatalf("backup spec is missing -%s", flagName)
		}
		assertEnvironmentNames(t, "backup", spec, environmentName)
	}

	for _, path := range []string{
		"auth list", "auth show", "auth new", "auth login",
		"profile list", "profile show", "profile new",
		"store list", "store show", "store new", "store verify", "store init",
		"__complete",
	} {
		command := commandByPath(t, path)
		spec, ok := command.flag("profiles-file")
		if !ok {
			t.Fatalf("%s spec is missing -profiles-file", path)
		}
		assertEnvironmentNames(t, path, spec, "CLOUDSTIC_PROFILES_FILE")
	}
}

func assertEnvironmentNames(t *testing.T, owner string, spec flagSpec, want ...string) {
	t.Helper()
	got := make([]string, 0, len(spec.environment))
	for _, environment := range spec.environment {
		got = append(got, environment.name)
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s -%s environments = %v, want %v", owner, spec.name, got, want)
	}
}

func commandByPath(t *testing.T, path string) *commandSpec {
	t.Helper()
	parts := strings.Fields(path)
	command := rootCommand(parts[0])
	if command == nil {
		t.Fatalf("command %q not found", path)
	}
	for _, part := range parts[1:] {
		command = findCommand(command.children, part)
		if command == nil {
			t.Fatalf("command %q not found", path)
		}
	}
	return command
}

func TestEnvironmentBindingsPreserveSupportedVariables(t *testing.T) {
	want := map[string]string{
		"CLOUDSTIC_STORE":                   "store",
		"CLOUDSTIC_PROFILE":                 "profile",
		"CLOUDSTIC_PROFILES_FILE":           "profiles-file",
		"CLOUDSTIC_S3_ENDPOINT":             "s3-endpoint",
		"CLOUDSTIC_S3_REGION":               "s3-region",
		"CLOUDSTIC_S3_PROFILE":              "s3-profile",
		"AWS_PROFILE":                       "s3-profile",
		"AWS_ACCESS_KEY_ID":                 "s3-access-key",
		"AWS_SECRET_ACCESS_KEY":             "s3-secret-key",
		"B2_KEY_ID":                         "b2-key-id",
		"B2_APP_KEY":                        "b2-app-key",
		"CLOUDSTIC_SOURCE_SFTP_PASSWORD":    "source-sftp-password",
		"CLOUDSTIC_SOURCE_SFTP_KEY":         "source-sftp-key",
		"CLOUDSTIC_SOURCE_SFTP_KNOWN_HOSTS": "source-sftp-known-hosts",
		"CLOUDSTIC_SOURCE_SFTP_INSECURE":    "source-sftp-insecure",
		"CLOUDSTIC_STORE_SFTP_PASSWORD":     "store-sftp-password",
		"CLOUDSTIC_STORE_SFTP_KEY":          "store-sftp-key",
		"CLOUDSTIC_STORE_SFTP_KNOWN_HOSTS":  "store-sftp-known-hosts",
		"CLOUDSTIC_STORE_SFTP_INSECURE":     "store-sftp-insecure",
		"CLOUDSTIC_ENCRYPTION_KEY":          "encryption-key",
		"CLOUDSTIC_PASSWORD":                "password",
		"CLOUDSTIC_RECOVERY_KEY":            "recovery-key",
		"CLOUDSTIC_KMS_KEY_ARN":             "kms-key-arn",
		"CLOUDSTIC_KMS_REGION":              "kms-region",
		"CLOUDSTIC_KMS_ENDPOINT":            "kms-endpoint",
		"CLOUDSTIC_DISABLE_PACKFILE":        "disable-packfile",
		"CLOUDSTIC_SOURCE":                  "source",
		"GOOGLE_APPLICATION_CREDENTIALS":    "google-credentials",
		"GOOGLE_CREDENTIALS_JSON":           "google-credentials-json",
		"GOOGLE_TOKEN_FILE":                 "google-token-file",
		"ONEDRIVE_CLIENT_ID":                "onedrive-client-id",
		"ONEDRIVE_TOKEN_FILE":               "onedrive-token-file",
		"CLOUDSTIC_CONFIG_DIR":              "",
		"CLOUDSTIC_VOLUME_UUID":             "volume-uuid",
	}
	got := environmentVariables()
	if len(got) != len(want) {
		t.Fatalf("environment binding count = %d, want %d", len(got), len(want))
	}
	for _, variable := range got {
		flagName, ok := want[variable.Name]
		if !ok {
			t.Errorf("unexpected environment variable %s", variable.Name)
		} else if variable.Flag != flagName {
			t.Errorf("%s flag = %q, want %q", variable.Name, variable.Flag, flagName)
		}
	}
}

func TestBuiltInDefaultsRemainIndependentFromFlagEnvironment(t *testing.T) {
	clearCommandEnvironment(t)
	configDir := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", configDir)

	a, err := parseBackupArgs(nil)
	if err != nil {
		t.Fatalf("parse backup defaults: %v", err)
	}
	wantProfilesFile := filepath.Join(configDir, defaultProfilesFilename)
	for name, values := range map[string][2]string{
		"store":                   {a.g.store, "local:./backup_store"},
		"profile":                 {a.g.profile, ""},
		"profiles-file":           {a.g.profilesFile, wantProfilesFile},
		"s3-endpoint":             {a.g.s3Endpoint, ""},
		"s3-region":               {a.g.s3Region, "us-east-1"},
		"s3-profile":              {a.g.s3Profile, ""},
		"s3-access-key":           {a.g.s3AccessKey, ""},
		"s3-secret-key":           {a.g.s3SecretKey, ""},
		"b2-key-id":               {a.g.b2KeyID, ""},
		"b2-app-key":              {a.g.b2AppKey, ""},
		"source-sftp-password":    {a.g.sourceSFTPPassword, ""},
		"source-sftp-key":         {a.g.sourceSFTPKey, ""},
		"source-sftp-known-hosts": {a.g.sourceSFTPKnownHosts, ""},
		"store-sftp-password":     {a.g.storeSFTPPassword, ""},
		"store-sftp-key":          {a.g.storeSFTPKey, ""},
		"store-sftp-known-hosts":  {a.g.storeSFTPKnownHosts, ""},
		"encryption-key":          {a.g.encryptionKey, ""},
		"password":                {a.g.password, ""},
		"recovery-key":            {a.g.recoveryKey, ""},
		"kms-key-arn":             {a.g.kmsKeyARN, ""},
		"kms-region":              {a.g.kmsRegion, ""},
		"kms-endpoint":            {a.g.kmsEndpoint, ""},
		"source":                  {a.sourceURI, "gdrive"},
	} {
		if values[0] != values[1] {
			t.Errorf("-%s default = %q, want %q", name, values[0], values[1])
		}
	}
	if a.g.sourceSFTPInsecure || a.g.storeSFTPInsecure || a.g.disablePackfile || a.g.prompt || a.g.noPrompt || a.g.verbose || a.g.quiet || a.g.json || a.g.debug {
		t.Error("boolean global defaults must remain false")
	}
	if a.volumeUUID != "" || a.googleCreds != "" || a.googleCredsJSON != "" || a.googleTokenFile != "" || a.onedriveClientID != "" || a.onedriveTokenFile != "" {
		t.Error("backup provider defaults must remain empty")
	}
}

func TestSuppliedFlagsOnlyContainsOverrides(t *testing.T) {
	flags := suppliedFlags(map[string]valueSource{
		"default":     valueSourceDefault,
		"profile":     valueSourceProfile,
		"environment": valueSourceEnvironment,
		"flag":        valueSourceFlag,
	})
	if len(flags) != 2 || !flags["environment"] || !flags["flag"] {
		t.Fatalf("supplied flags = %#v", flags)
	}
}

func TestEnvironmentBindingsFollowTheActiveCommandSpec(t *testing.T) {
	clearCommandEnvironment(t)
	t.Setenv("CLOUDSTIC_S3_REGION", "environment-region")

	globalSet := flag.NewFlagSet("backup", flag.ContinueOnError)
	global := addGlobalFlags(globalSet)
	if err := parseFlags(globalSet, nil, backupCommandSpec()); err != nil {
		t.Fatalf("parse global flags: %v", err)
	}
	if global.s3Region != "environment-region" {
		t.Fatalf("global s3 region = %q, want environment-region", global.s3Region)
	}

	localSet := flag.NewFlagSet("store new", flag.ContinueOnError)
	region := localSet.String("s3-region", "local-default", "S3 region")
	if err := parseFlags(localSet, nil, storeNewCommandSpec()); err != nil {
		t.Fatalf("parse store new flags: %v", err)
	}
	if *region != "local-default" {
		t.Fatalf("store new s3 region = %q, want local-default", *region)
	}
}

func TestEnvironmentFallbackOrderComesFromFlagSpec(t *testing.T) {
	clearCommandEnvironment(t)
	t.Setenv("AWS_PROFILE", "provider-profile")
	t.Setenv("CLOUDSTIC_S3_PROFILE", "cloudstic-profile")

	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	global := addGlobalFlags(fs)
	if err := parseFlags(fs, nil, backupCommandSpec()); err != nil {
		t.Fatalf("parse global flags: %v", err)
	}
	if global.s3Profile != "cloudstic-profile" {
		t.Fatalf("s3 profile = %q, want cloudstic-profile", global.s3Profile)
	}

	t.Setenv("CLOUDSTIC_S3_PROFILE", "")
	fs = flag.NewFlagSet("backup", flag.ContinueOnError)
	global = addGlobalFlags(fs)
	if err := parseFlags(fs, nil, backupCommandSpec()); err != nil {
		t.Fatalf("parse global flags with provider fallback: %v", err)
	}
	if global.s3Profile != "provider-profile" {
		t.Fatalf("s3 profile = %q, want provider-profile", global.s3Profile)
	}
}

func TestProfileNewEnvironmentValuesOverrideExistingProfile(t *testing.T) {
	clearCommandEnvironment(t)
	t.Setenv("CLOUDSTIC_SOURCE", "local:/environment")
	t.Setenv("CLOUDSTIC_STORE", "s3:environment")
	t.Setenv("GOOGLE_TOKEN_FILE", "/environment/token.json")

	a, err := parseProfileNewArgs(nil)
	if err != nil {
		t.Fatalf("parse profile new: %v", err)
	}
	prefillProfileArgs(a, cloudstic.BackupProfile{
		Source:          "local:/profile",
		GoogleTokenFile: "/profile/token.json",
	})
	if a.source != "local:/environment" || a.googleTokenFile != "/environment/token.json" || a.store != "s3:environment" {
		t.Fatalf("profile environment values were overwritten: source=%q token=%q store=%q", a.source, a.googleTokenFile, a.store)
	}
	for _, name := range []string{"source", "store", "google-token-file"} {
		if !a.flagsSet[name] {
			t.Errorf("environment-provided -%s was not marked as supplied", name)
		}
	}
}

func TestEnvironment_SecretValuesNeverAppearInHelp(t *testing.T) {
	clearCommandEnvironment(t)
	const secret = "sentinel-secret-that-must-not-appear"
	for _, variable := range environmentVariables() {
		if variable.Sensitive {
			t.Setenv(variable.Name, secret)
		}
	}
	fs := flag.NewFlagSet("secrets", flag.ContinueOnError)
	var output bytes.Buffer
	fs.SetOutput(&output)
	command := backupCommandSpec()
	for _, spec := range command.effectiveFlags() {
		if len(spec.environment) == 0 {
			continue
		}
		if spec.takesValue() {
			fs.String(spec.name, "built-in", spec.description)
		} else {
			fs.Bool(spec.name, false, spec.description)
		}
	}
	err := parseFlags(fs, []string{"-h"}, command)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags(-h) error = %v, want flag.ErrHelp", err)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("help exposed a secret environment value:\n%s", output.String())
	}
	for _, variable := range environmentVariables() {
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
	err := parseFlags(fs, []string{"-h"}, backupCommandSpec())
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
			if err := parseFlags(fs, nil, breakLockCommandSpec()); err != nil {
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
	err := parseFlags(fs, nil, breakLockCommandSpec())
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
	if err := parseFlags(fs, []string{"-profile", "daily", "-profiles-file", profilesPath}, backupCommandSpec()); err != nil {
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
	if err := parseFlags(fs, []string{"-s3-region", "flag-region"}, backupCommandSpec()); err != nil {
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
	if err := parseFlags(fs, []string{"-b2-key-id", "flag-key-id"}, backupCommandSpec()); err != nil {
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
			t.Fatalf("%s environment-variable documentation is stale; regenerate it from the command flag specs\n--- want ---\n%s--- got ---\n%s", name, want, got)
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
