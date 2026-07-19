package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/cloudstic/cli/internal/ui"
)

type valueSource string

const (
	valueSourceDefault     valueSource = "default"
	valueSourceProfile     valueSource = "profile"
	valueSourceEnvironment valueSource = "environment"
	valueSourceFlag        valueSource = "flag"
)

type globalFlags struct {
	store                             string
	profile, profilesFile             string
	s3Endpoint, s3Region, s3Profile   string
	s3AccessKey, s3SecretKey          string
	b2KeyID, b2AppKey                 string
	sourceSFTPPassword, sourceSFTPKey string
	sourceSFTPInsecure                bool
	sourceSFTPKnownHosts              string
	storeSFTPPassword, storeSFTPKey   string
	storeSFTPInsecure                 bool
	storeSFTPKnownHosts               string
	encryptionKey                     string
	password                          string
	recoveryKey                       string
	kmsKeyARN, kmsRegion, kmsEndpoint string
	disablePackfile                   bool
	prompt, noPrompt                  bool
	verbose, quiet, debug             bool
	json                              bool
	debugLog                          *ui.SafeLogWriter
	flagSet                           *flag.FlagSet
	sources                           map[string]valueSource
}

var globalFlagSpecs = []flagSpec{
	storeFlag(),
	valueFlag("profile", "name", "Backup profile name", completionProfile).withEnvironment(environment("CLOUDSTIC_PROFILE")),
	profilesFileFlag(),
	valueFlag("s3-endpoint", "url", "S3-compatible endpoint URL", completionNone).withEnvironment(environment("CLOUDSTIC_S3_ENDPOINT")),
	valueFlag("s3-region", "region", "S3 region", completionNone).withEnvironment(environment("CLOUDSTIC_S3_REGION")),
	valueFlag("s3-profile", "name", "AWS shared config profile", completionNone).withEnvironment(
		environment("CLOUDSTIC_S3_PROFILE"), environment("AWS_PROFILE")),
	valueFlag("s3-access-key", "key", "S3 access key ID", completionNone).withEnvironment(sensitiveEnvironment("AWS_ACCESS_KEY_ID")),
	valueFlag("s3-secret-key", "secret", "S3 secret access key", completionNone).withEnvironment(sensitiveEnvironment("AWS_SECRET_ACCESS_KEY")),
	valueFlag("b2-key-id", "key", "Backblaze B2 application key ID", completionNone).withEnvironment(sensitiveEnvironment("B2_KEY_ID")),
	valueFlag("b2-app-key", "secret", "Backblaze B2 application key", completionNone).withEnvironment(sensitiveEnvironment("B2_APP_KEY")),
	valueFlag("source-sftp-password", "password", "SFTP source password", completionNone).withEnvironment(sensitiveEnvironment("CLOUDSTIC_SOURCE_SFTP_PASSWORD")),
	valueFlag("source-sftp-key", "path", "SSH private key for SFTP source", completionFile).withEnvironment(sensitiveEnvironment("CLOUDSTIC_SOURCE_SFTP_KEY")),
	valueFlag("source-sftp-known-hosts", "path", "known_hosts file for SFTP source", completionFile).withEnvironment(environment("CLOUDSTIC_SOURCE_SFTP_KNOWN_HOSTS")),
	boolFlag("source-sftp-insecure", "Skip host-key validation for SFTP source (INSECURE)").withEnvironment(environment("CLOUDSTIC_SOURCE_SFTP_INSECURE")),
	valueFlag("store-sftp-password", "password", "SFTP store password", completionNone).withEnvironment(sensitiveEnvironment("CLOUDSTIC_STORE_SFTP_PASSWORD")),
	valueFlag("store-sftp-key", "path", "SSH private key for SFTP store", completionFile).withEnvironment(sensitiveEnvironment("CLOUDSTIC_STORE_SFTP_KEY")),
	valueFlag("store-sftp-known-hosts", "path", "known_hosts file for SFTP store", completionFile).withEnvironment(environment("CLOUDSTIC_STORE_SFTP_KNOWN_HOSTS")),
	boolFlag("store-sftp-insecure", "Skip host-key validation for SFTP store (INSECURE)").withEnvironment(environment("CLOUDSTIC_STORE_SFTP_INSECURE")),
	valueFlag("encryption-key", "hex", "Platform encryption key", completionNone).withEnvironment(sensitiveEnvironment("CLOUDSTIC_ENCRYPTION_KEY")).inSection(flagSectionEncryption),
	valueFlag("password", "password", "Repository password", completionNone).withEnvironment(sensitiveEnvironment("CLOUDSTIC_PASSWORD")).inSection(flagSectionEncryption),
	valueFlag("recovery-key", "words", "Recovery key mnemonic", completionNone).withEnvironment(sensitiveEnvironment("CLOUDSTIC_RECOVERY_KEY")).inSection(flagSectionEncryption),
	valueFlag("kms-key-arn", "arn", "AWS KMS key ARN", completionNone).withEnvironment(environment("CLOUDSTIC_KMS_KEY_ARN")).inSection(flagSectionEncryption),
	valueFlag("kms-region", "region", "AWS KMS region", completionNone).withEnvironment(environment("CLOUDSTIC_KMS_REGION")).inSection(flagSectionEncryption),
	valueFlag("kms-endpoint", "url", "AWS KMS endpoint URL", completionNone).withEnvironment(environment("CLOUDSTIC_KMS_ENDPOINT")).inSection(flagSectionEncryption),
	boolFlag("disable-packfile", "Disable small-object packfiles").withEnvironment(environment("CLOUDSTIC_DISABLE_PACKFILE")),
	boolFlag("prompt", "Prompt for a repository password").inSection(flagSectionEncryption),
	boolFlag("no-prompt", "Disable interactive prompts"),
	boolFlag("verbose", "Log detailed operations"),
	boolFlag("quiet", "Suppress progress bars"),
	boolFlag("json", "Write command result as JSON"),
	boolFlag("debug", "Log every store request"),
}

func storeFlag() flagSpec {
	return storeURIFlag("store", "Storage backend URI").withEnvironment(environment("CLOUDSTIC_STORE"))
}

func profilesFileFlag() flagSpec {
	return valueFlag("profiles-file", "path", "Path to profiles YAML file", completionFile).withEnvironment(environment("CLOUDSTIC_PROFILES_FILE"))
}

func sourceFlag() flagSpec {
	return sourceURIFlag("source", "Source URI").withEnvironment(environment("CLOUDSTIC_SOURCE"))
}

func storeURIFlag(name, description string) flagSpec {
	return valueFlag(name, "uri", description, completionNone).withCompletionValues("local:", "s3:", "b2:", "sftp://")
}

func sourceURIFlag(name, description string) flagSpec {
	return valueFlag(name, "uri", description, completionNone).withCompletionValues(
		"local:", "sftp://", "gdrive", "gdrive-changes", "onedrive", "onedrive-changes")
}

func volumeUUIDFlag() flagSpec {
	return valueFlag("volume-uuid", "uuid", "Local volume UUID", completionNone).withEnvironment(hiddenEnvironment("CLOUDSTIC_VOLUME_UUID"))
}

func googleCredentialsFlag() flagSpec {
	return valueFlag("google-credentials", "path", "Google credentials file", completionFile).withEnvironment(
		sensitiveEnvironment("GOOGLE_APPLICATION_CREDENTIALS"))
}

func googleCredentialsJSONFlag() flagSpec {
	return valueFlag("google-credentials-json", "json", "Inline Google credentials", completionNone).withEnvironment(
		sensitiveEnvironment("GOOGLE_CREDENTIALS_JSON"))
}

func googleTokenFileFlag() flagSpec {
	return valueFlag("google-token-file", "path", "Google OAuth token file", completionFile).withEnvironment(
		sensitiveEnvironment("GOOGLE_TOKEN_FILE"))
}

func onedriveClientIDFlag() flagSpec {
	return valueFlag("onedrive-client-id", "id", "OneDrive client ID", completionNone).withEnvironment(
		environment("ONEDRIVE_CLIENT_ID"))
}

func onedriveTokenFileFlag() flagSpec {
	return valueFlag("onedrive-token-file", "path", "OneDrive token file", completionFile).withEnvironment(
		sensitiveEnvironment("ONEDRIVE_TOKEN_FILE"))
}

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{flagSet: fs}
	fs.StringVar(&g.store, "store", "local:./backup_store", "Storage backend URI: local:<path>, s3:<bucket>[/<prefix>], b2:<bucket>[/<prefix>], sftp://[user@]host[:port]/<path>")
	defaultProfilesPath, err := defaultProfilesPath()
	if err != nil {
		defaultProfilesPath = defaultProfilesFilename
	}
	fs.StringVar(&g.profile, "profile", "", "Profile name from profiles.yaml")
	fs.StringVar(&g.profilesFile, "profiles-file", defaultProfilesPath, "Path to profiles YAML file")
	fs.StringVar(&g.s3Endpoint, "s3-endpoint", "", "S3 compatible endpoint URL (for MinIO, R2, etc.)")
	fs.StringVar(&g.s3Region, "s3-region", "us-east-1", "S3 region")
	fs.StringVar(&g.s3Profile, "s3-profile", "", "AWS shared config profile for S3 credentials")
	fs.StringVar(&g.s3AccessKey, "s3-access-key", "", "S3 access key ID")
	fs.StringVar(&g.s3SecretKey, "s3-secret-key", "", "S3 secret access key")
	fs.StringVar(&g.b2KeyID, "b2-key-id", "", "Backblaze B2 application key ID")
	fs.StringVar(&g.b2AppKey, "b2-app-key", "", "Backblaze B2 application key")

	fs.StringVar(&g.sourceSFTPPassword, "source-sftp-password", "", "SFTP source password")
	fs.StringVar(&g.sourceSFTPKey, "source-sftp-key", "", "Path to SSH private key for SFTP source")
	fs.BoolVar(&g.sourceSFTPInsecure, "source-sftp-insecure", false, "Skip host key validation for SFTP source (INSECURE)")
	fs.StringVar(&g.sourceSFTPKnownHosts, "source-sftp-known-hosts", "", "Path to known_hosts file for SFTP source")

	fs.StringVar(&g.storeSFTPPassword, "store-sftp-password", "", "SFTP store password")
	fs.StringVar(&g.storeSFTPKey, "store-sftp-key", "", "Path to SSH private key for SFTP store")
	fs.BoolVar(&g.storeSFTPInsecure, "store-sftp-insecure", false, "Skip host key validation for SFTP store (INSECURE)")
	fs.StringVar(&g.storeSFTPKnownHosts, "store-sftp-known-hosts", "", "Path to known_hosts file for SFTP store")

	fs.StringVar(&g.encryptionKey, "encryption-key", "", "Platform key (hex-encoded, 32 bytes)")
	fs.StringVar(&g.password, "password", "", "Repository password")
	fs.StringVar(&g.recoveryKey, "recovery-key", "", "Recovery key (BIP39 24-word mnemonic)")
	fs.StringVar(&g.kmsKeyARN, "kms-key-arn", "", "AWS KMS key ARN for kms-platform slots")
	fs.StringVar(&g.kmsRegion, "kms-region", "", "AWS KMS region (defaults to us-east-1)")
	fs.StringVar(&g.kmsEndpoint, "kms-endpoint", "", "Custom AWS KMS endpoint URL")
	fs.BoolVar(&g.disablePackfile, "disable-packfile", false, "Disable bundling small objects into 8MB packs")
	fs.BoolVar(&g.prompt, "prompt", false, "Prompt for password interactively (use alongside --encryption-key or --kms-key-arn to add a password layer)")
	fs.BoolVar(&g.noPrompt, "no-prompt", false, "Disable interactive prompts (for scripts and CI)")
	fs.BoolVar(&g.verbose, "verbose", false, "Log detailed file-level operations")
	fs.BoolVar(&g.quiet, "quiet", false, "Suppress progress bars (keeps final summary)")
	fs.BoolVar(&g.json, "json", false, "Write command result as JSON to stdout")
	fs.BoolVar(&g.debug, "debug", false, "Log every store request (network calls, timing, sizes)")
	return g
}

func (g *globalFlags) jsonEnabled() bool {
	return g != nil && g.json
}

func (g *globalFlags) valueSource(name string) valueSource {
	if source := g.sources[name]; source != "" {
		return source
	}
	return flagValueSource(g.flagSet, globalFlagByName(name))
}

func (g *globalFlags) setValueSource(name string, source valueSource) {
	if g.sources == nil {
		g.sources = make(map[string]valueSource)
	}
	g.sources[name] = source
}

func environmentValue(spec flagSpec) (string, bool) {
	for _, environment := range spec.environment {
		if value, ok := nonEmptyEnvironment(environment.name); ok {
			return value, true
		}
	}
	return "", false
}

func nonEmptyEnvironment(name string) (string, bool) {
	value, ok := os.LookupEnv(name)
	return value, ok && value != ""
}

func annotateEnvironmentUsage(fs *flag.FlagSet, specs []flagSpec) {
	for _, spec := range specs {
		f := fs.Lookup(spec.name)
		if f == nil || len(spec.environment) == 0 || strings.Contains(f.Usage, "env: ") {
			continue
		}
		names := make([]string, 0, len(spec.environment))
		for _, environment := range spec.environment {
			names = append(names, environment.name)
		}
		f.Usage += " (env: " + strings.Join(names, ", ") + ")"
	}
}

func applyEnvironment(fs *flag.FlagSet, specs []flagSpec) error {
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for _, spec := range specs {
		if explicit[spec.name] || len(spec.environment) == 0 {
			continue
		}
		f := fs.Lookup(spec.name)
		if f == nil {
			continue
		}
		for _, environment := range spec.environment {
			raw, ok := nonEmptyEnvironment(environment.name)
			if !ok {
				continue
			}
			if isBooleanFlag(f) {
				parsed, err := strconv.ParseBool(raw)
				if err != nil {
					return fmt.Errorf("invalid boolean value %q for %s: %w", raw, environment.name, err)
				}
				raw = strconv.FormatBool(parsed)
			}
			if err := f.Value.Set(raw); err != nil {
				return fmt.Errorf("apply %s to -%s: %w", environment.name, spec.name, err)
			}
			break
		}
	}
	return nil
}

func isBooleanFlag(f *flag.Flag) bool {
	boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && boolean.IsBoolFlag()
}

func flagValueSource(fs *flag.FlagSet, spec flagSpec) valueSource {
	if fs == nil {
		return valueSourceDefault
	}
	explicit := false
	fs.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == spec.name })
	if explicit {
		return valueSourceFlag
	}
	if _, ok := environmentValue(spec); ok {
		return valueSourceEnvironment
	}
	return valueSourceDefault
}

func commandValueSources(fs *flag.FlagSet, command *commandSpec) map[string]valueSource {
	sources := make(map[string]valueSource)
	fs.VisitAll(func(f *flag.Flag) {
		spec, ok := command.flag(f.Name)
		if ok {
			sources[f.Name] = flagValueSource(fs, spec)
		}
	})
	return sources
}

func suppliedFlags(sources map[string]valueSource) map[string]bool {
	flags := make(map[string]bool)
	for name, source := range sources {
		if source == valueSourceFlag || source == valueSourceEnvironment {
			flags[name] = true
		}
	}
	return flags
}

// parseFlags parses args into fs, reordering positional arguments after
// flags so that flags can appear anywhere on the command line (e.g.
// "cloudstic restore abc123 -output ./out.zip" works as well as the reverse).
// Using this consistently means every command supports flexible argument ordering.
func parseFlags(fs *flag.FlagSet, args []string, command *commandSpec) error {
	if fs.Lookup("no-prompt") == nil {
		fs.Bool("no-prompt", false, "Disable interactive prompts (for scripts and CI)")
	}
	if hasGlobalFlag(args, "json") && !hasGlobalFlag(args, "h") {
		fs.SetOutput(io.Discard)
	}
	var specs []flagSpec
	if command != nil {
		specs = command.effectiveFlags()
	}
	annotateEnvironmentUsage(fs, specs)
	if flagSetObserver != nil {
		flagSetObserver(fs)
	}
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	return applyEnvironment(fs, specs)
}

func (r *runner) parseError(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return r.fail("Error: %v", err)
}

// reorderArgs moves flag arguments before positional arguments so that Go's
// flag package (which stops at the first non-flag) parses all flags regardless
// of where they appear on the command line.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	terminated := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			terminated = true
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		if strings.Contains(arg, "=") {
			continue
		}
		name := strings.TrimLeft(arg, "-")
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if terminated {
		flags = append(flags, "--")
	}
	return append(flags, positional...)
}

// stringArrayFlags implements flag.Value for repeatable string flags.
type stringArrayFlags []string

func (i *stringArrayFlags) String() string {
	return fmt.Sprint(*i)
}

func (i *stringArrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}
