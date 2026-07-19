package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type valueSource string

const maxEnvironmentDestinations = 64

const (
	valueSourceDefault     valueSource = "default"
	valueSourceProfile     valueSource = "profile"
	valueSourceEnvironment valueSource = "environment"
	valueSourceFlag        valueSource = "flag"
)

type environmentVariable struct {
	Name             string
	Flag             string
	Description      string
	Sensitive        bool
	Boolean          bool
	ProviderStandard bool
	GlobalOnly       bool
	Documented       bool
}

// environmentVariables is the single inventory of environment variables
// consumed by command flag parsing. Entries sharing a flag are fallbacks in
// declaration order (for example CLOUDSTIC_S3_PROFILE before AWS_PROFILE).
// Variables without a flag are consumed by lower-level configuration helpers.
var environmentVariables = []environmentVariable{
	{Name: "CLOUDSTIC_STORE", Flag: "store", Description: "Storage backend URI", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_PROFILE", Flag: "profile", Description: "Backup profile name", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_PROFILES_FILE", Flag: "profiles-file", Description: "Path to profiles YAML file", Documented: true},
	{Name: "CLOUDSTIC_S3_ENDPOINT", Flag: "s3-endpoint", Description: "S3-compatible endpoint URL", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_S3_REGION", Flag: "s3-region", Description: "S3 region", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_S3_PROFILE", Flag: "s3-profile", Description: "AWS shared config profile", GlobalOnly: true, Documented: true},
	{Name: "AWS_PROFILE", Flag: "s3-profile", Description: "AWS shared config profile", ProviderStandard: true, GlobalOnly: true, Documented: true},
	{Name: "AWS_ACCESS_KEY_ID", Flag: "s3-access-key", Description: "S3 access key ID", Sensitive: true, ProviderStandard: true, GlobalOnly: true, Documented: true},
	{Name: "AWS_SECRET_ACCESS_KEY", Flag: "s3-secret-key", Description: "S3 secret access key", Sensitive: true, ProviderStandard: true, GlobalOnly: true, Documented: true},
	{Name: "B2_KEY_ID", Flag: "b2-key-id", Description: "Backblaze B2 application key ID", Sensitive: true, ProviderStandard: true, GlobalOnly: true, Documented: true},
	{Name: "B2_APP_KEY", Flag: "b2-app-key", Description: "Backblaze B2 application key", Sensitive: true, ProviderStandard: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_SOURCE_SFTP_PASSWORD", Flag: "source-sftp-password", Description: "SFTP source password", Sensitive: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_SOURCE_SFTP_KEY", Flag: "source-sftp-key", Description: "SFTP source private key path", Sensitive: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_SOURCE_SFTP_INSECURE", Flag: "source-sftp-insecure", Description: "Disable SFTP source host-key verification", Boolean: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_SOURCE_SFTP_KNOWN_HOSTS", Flag: "source-sftp-known-hosts", Description: "SFTP source known_hosts path", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_STORE_SFTP_PASSWORD", Flag: "store-sftp-password", Description: "SFTP store password", Sensitive: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_STORE_SFTP_KEY", Flag: "store-sftp-key", Description: "SFTP store private key path", Sensitive: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_STORE_SFTP_INSECURE", Flag: "store-sftp-insecure", Description: "Disable SFTP store host-key verification", Boolean: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_STORE_SFTP_KNOWN_HOSTS", Flag: "store-sftp-known-hosts", Description: "SFTP store known_hosts path", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_ENCRYPTION_KEY", Flag: "encryption-key", Description: "Platform encryption key", Sensitive: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_PASSWORD", Flag: "password", Description: "Repository password", Sensitive: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_RECOVERY_KEY", Flag: "recovery-key", Description: "Recovery seed phrase", Sensitive: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_KMS_KEY_ARN", Flag: "kms-key-arn", Description: "AWS KMS key ARN", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_KMS_REGION", Flag: "kms-region", Description: "AWS KMS region", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_KMS_ENDPOINT", Flag: "kms-endpoint", Description: "AWS KMS endpoint URL", GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_DISABLE_PACKFILE", Flag: "disable-packfile", Description: "Disable packfiles", Boolean: true, GlobalOnly: true, Documented: true},
	{Name: "CLOUDSTIC_SOURCE", Flag: "source", Description: "Backup source URI", Documented: true},
	{Name: "GOOGLE_APPLICATION_CREDENTIALS", Flag: "google-credentials", Description: "Google credentials file", Sensitive: true, ProviderStandard: true, Documented: true},
	{Name: "GOOGLE_CREDENTIALS_JSON", Flag: "google-credentials-json", Description: "Inline Google credentials JSON", Sensitive: true, ProviderStandard: true, Documented: true},
	{Name: "GOOGLE_TOKEN_FILE", Flag: "google-token-file", Description: "Google OAuth token file", Sensitive: true, ProviderStandard: true, Documented: true},
	{Name: "ONEDRIVE_CLIENT_ID", Flag: "onedrive-client-id", Description: "OneDrive OAuth client ID", ProviderStandard: true, Documented: true},
	{Name: "ONEDRIVE_TOKEN_FILE", Flag: "onedrive-token-file", Description: "OneDrive OAuth token file", Sensitive: true, ProviderStandard: true, Documented: true},
	{Name: "CLOUDSTIC_CONFIG_DIR", Description: "Cloudstic configuration directory", Documented: true},
	{Name: "CLOUDSTIC_VOLUME_UUID", Flag: "volume-uuid", Description: "Test override for local volume identity"},
}

func environmentForFlag(name string) []environmentVariable {
	var matches []environmentVariable
	for _, variable := range environmentVariables {
		if variable.Flag == name {
			matches = append(matches, variable)
		}
	}
	return matches
}

func applicableEnvironmentForFlag(fs *flag.FlagSet, name string) []environmentVariable {
	variables := environmentForFlag(name)
	if fs.Lookup("store") != nil {
		return variables
	}
	kept := variables[:0]
	for _, variable := range variables {
		if !variable.GlobalOnly {
			kept = append(kept, variable)
		}
	}
	return kept
}

func environmentFlagIndex(name string) (int, bool) {
	index := 0
	seen := map[string]bool{}
	for _, variable := range environmentVariables {
		if variable.Flag == "" || seen[variable.Flag] {
			continue
		}
		if variable.Flag == name {
			return index, index < maxEnvironmentDestinations
		}
		seen[variable.Flag] = true
		index++
	}
	return 0, false
}

func environmentValue(flagName string) (string, bool) {
	for _, variable := range environmentForFlag(flagName) {
		if value, ok := os.LookupEnv(variable.Name); ok && value != "" {
			return value, true
		}
	}
	return "", false
}

func namedEnvironmentValue(name string) (string, bool) {
	for _, variable := range environmentVariables {
		if variable.Name != name {
			continue
		}
		value, ok := os.LookupEnv(name)
		return value, ok && value != ""
	}
	return "", false
}

func annotateEnvironmentUsage(fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		variables := applicableEnvironmentForFlag(fs, f.Name)
		if len(variables) == 0 || strings.Contains(f.Usage, "env: ") {
			return
		}
		names := make([]string, 0, len(variables))
		for _, variable := range variables {
			names = append(names, variable.Name)
		}
		f.Usage += " (env: " + strings.Join(names, ", ") + ")"
	})
}

func applyEnvironment(fs *flag.FlagSet) error {
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	hasGlobalFlags := fs.Lookup("store") != nil
	for _, variable := range environmentVariables {
		if variable.GlobalOnly && !hasGlobalFlags {
			continue
		}
		if variable.Flag == "" || explicit[variable.Flag] {
			continue
		}
		f := fs.Lookup(variable.Flag)
		if f == nil {
			continue
		}
		raw, ok := os.LookupEnv(variable.Name)
		if !ok || raw == "" {
			continue
		}
		if variable.Boolean {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return fmt.Errorf("invalid boolean value %q for %s: %w", raw, variable.Name, err)
			}
			raw = strconv.FormatBool(parsed)
		}
		if err := f.Value.Set(raw); err != nil {
			return fmt.Errorf("apply %s to -%s: %w", variable.Name, variable.Flag, err)
		}
		explicit[variable.Flag] = true
	}
	return nil
}

func flagValueSource(fs *flag.FlagSet, name string) valueSource {
	if fs == nil {
		return valueSourceDefault
	}
	explicit := false
	fs.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == name })
	if explicit {
		return valueSourceFlag
	}
	for _, variable := range applicableEnvironmentForFlag(fs, name) {
		if value, ok := os.LookupEnv(variable.Name); ok && value != "" {
			return valueSourceEnvironment
		}
	}
	return valueSourceDefault
}

func environmentDocumentation() string {
	var out strings.Builder
	out.WriteString("Values resolve in this order: explicit CLI flag, environment variable, profile, built-in default.\n\n")
	for _, variable := range environmentVariables {
		if variable.ProviderStandard {
			out.WriteString("Provider-oriented variables remain deliberately unprefixed for compatibility with their existing ecosystems. Sensitive values are never rendered in help output.\n\n")
			break
		}
	}
	out.WriteString("| Variable | Flag equivalent | Description |\n")
	out.WriteString("| :--- | :--- | :--- |\n")
	for _, variable := range environmentVariables {
		if !variable.Documented {
			continue
		}
		flagName := "—"
		if variable.Flag != "" {
			flagName = "`-" + variable.Flag + "`"
		}
		description := variable.Description
		if variable.Sensitive {
			description += " (sensitive)"
		}
		_, _ = fmt.Fprintf(&out, "| `%s` | %s | %s |\n", variable.Name, flagName, description)
	}
	return out.String()
}
