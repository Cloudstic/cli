package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/cloudstic/cli/internal/ui"
)

type globalFlags struct {
	store                             string
	profile, profilesFile             string
	s3Endpoint, s3Region, s3Profile   string
	s3AccessKey, s3SecretKey          string
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
}

// Global flags are declared as named groups that commands opt into, so a
// command's help output lists only the flags it can actually act on. Every
// group is a function of the destination struct: the specs bind directly into
// the globalFlags value the command will read.

// repoFlagSpecs covers locating and addressing the repository itself.
func repoFlagSpecs(g *globalFlags) []flagSpec {
	defaultProfilesPath, err := defaultProfilesPath()
	if err != nil {
		defaultProfilesPath = defaultProfilesFilename
	}
	return []flagSpec{
		stringFlag(&g.store, "store", "local:./backup_store",
			"Storage backend URI: local:<path>, s3:<bucket>[/<prefix>], b2:<bucket>[/<prefix>], sftp://[user@]host[:port]/<path>",
			withEnv("CLOUDSTIC_STORE"), withPlaceholder("<uri>"), withCompleter("_cloudstic_store_prefixes"),
			withShortUsage("Storage backend URI")),
		stringFlag(&g.profile, "profile", "", "Profile name from profiles.yaml",
			withEnv("CLOUDSTIC_PROFILE"), withPlaceholder("<name>"), withCompleter("_cloudstic_profile_names")),
		stringFlag(&g.profilesFile, "profiles-file", defaultProfilesPath, "Path to profiles YAML file",
			withEnv("CLOUDSTIC_PROFILES_FILE"), withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&g.s3Endpoint, "s3-endpoint", "", "S3 compatible endpoint URL (for MinIO, R2, etc.)",
			withEnv("CLOUDSTIC_S3_ENDPOINT"), withPlaceholder("<url>"),
			withShortUsage("S3 compatible endpoint URL")),
		stringFlag(&g.s3Region, "s3-region", "us-east-1", "S3 region",
			withEnv("CLOUDSTIC_S3_REGION"), withPlaceholder("<region>")),
		stringFlag(&g.s3Profile, "s3-profile", envDefault("AWS_PROFILE", ""), "AWS shared config profile for S3 credentials",
			withEnv("CLOUDSTIC_S3_PROFILE"), withPlaceholder("<name>")),
		stringFlag(&g.s3AccessKey, "s3-access-key", "", "S3 access key ID",
			withEnv("AWS_ACCESS_KEY_ID"), withPlaceholder("<key>"), asSecret()),
		stringFlag(&g.s3SecretKey, "s3-secret-key", "", "S3 secret access key",
			withEnv("AWS_SECRET_ACCESS_KEY"), withPlaceholder("<secret>"), asSecret()),
		boolFlag(&g.disablePackfile, "disable-packfile", false, "Disable bundling small objects into 8MB packs",
			withEnv("CLOUDSTIC_DISABLE_PACKFILE"), withShortUsage("Disable bundling small objects into packs")),
	}
}

// storeSFTPFlagSpecs covers SFTP credentials for the repository store. Every
// repository command needs these, since any store URI may be sftp://.
func storeSFTPFlagSpecs(g *globalFlags) []flagSpec {
	return []flagSpec{
		stringFlag(&g.storeSFTPPassword, "store-sftp-password", "", "SFTP store password",
			withEnv("CLOUDSTIC_STORE_SFTP_PASSWORD"), withPlaceholder("<pw>"), asSecret()),
		stringFlag(&g.storeSFTPKey, "store-sftp-key", "", "Path to SSH private key for SFTP store",
			withEnv("CLOUDSTIC_STORE_SFTP_KEY"), withPlaceholder("<path>"), withCompleter("_files")),
		boolFlag(&g.storeSFTPInsecure, "store-sftp-insecure", false, "Skip host key validation for SFTP store (INSECURE)",
			withEnv("CLOUDSTIC_STORE_SFTP_INSECURE")),
		stringFlag(&g.storeSFTPKnownHosts, "store-sftp-known-hosts", "", "Path to known_hosts file for SFTP store",
			withEnv("CLOUDSTIC_STORE_SFTP_KNOWN_HOSTS"), withPlaceholder("<path>"), withCompleter("_files")),
	}
}

// sourceSFTPFlagSpecs covers SFTP credentials for a backup *source*. Only
// commands that read a source (backup) need these.
func sourceSFTPFlagSpecs(g *globalFlags) []flagSpec {
	return []flagSpec{
		stringFlag(&g.sourceSFTPPassword, "source-sftp-password", "", "SFTP source password",
			withEnv("CLOUDSTIC_SOURCE_SFTP_PASSWORD"), withPlaceholder("<pw>"), asSecret()),
		stringFlag(&g.sourceSFTPKey, "source-sftp-key", "", "Path to SSH private key for SFTP source",
			withEnv("CLOUDSTIC_SOURCE_SFTP_KEY"), withPlaceholder("<path>"), withCompleter("_files")),
		boolFlag(&g.sourceSFTPInsecure, "source-sftp-insecure", false, "Skip host key validation for SFTP source (INSECURE)",
			withEnv("CLOUDSTIC_SOURCE_SFTP_INSECURE")),
		stringFlag(&g.sourceSFTPKnownHosts, "source-sftp-known-hosts", "", "Path to known_hosts file for SFTP source",
			withEnv("CLOUDSTIC_SOURCE_SFTP_KNOWN_HOSTS"), withPlaceholder("<path>"), withCompleter("_files")),
	}
}

// encryptionFlagSpecs covers credentials for unlocking an encrypted repository.
func encryptionFlagSpecs(g *globalFlags) []flagSpec {
	return []flagSpec{
		stringFlag(&g.password, "password", "", "Repository password",
			withEnv("CLOUDSTIC_PASSWORD"), withPlaceholder("<pw>"), asSecret()),
		stringFlag(&g.encryptionKey, "encryption-key", "", "Platform key (hex-encoded, 32 bytes)",
			withEnv("CLOUDSTIC_ENCRYPTION_KEY"), withPlaceholder("<hex>"), asSecret(),
			withShortUsage("Platform key (hex-encoded)")),
		stringFlag(&g.recoveryKey, "recovery-key", "", "Recovery key (BIP39 24-word mnemonic)",
			withEnv("CLOUDSTIC_RECOVERY_KEY"), withPlaceholder("<words>"), asSecret(),
			withShortUsage("Recovery key (24-word mnemonic)")),
		stringFlag(&g.kmsKeyARN, "kms-key-arn", "", "AWS KMS key ARN for kms-platform slots",
			withEnv("CLOUDSTIC_KMS_KEY_ARN"), withPlaceholder("<arn>"),
			withShortUsage("AWS KMS key ARN")),
		stringFlag(&g.kmsRegion, "kms-region", "", "AWS KMS region (defaults to us-east-1)",
			withEnv("CLOUDSTIC_KMS_REGION"), withPlaceholder("<region>"),
			withShortUsage("AWS KMS region")),
		stringFlag(&g.kmsEndpoint, "kms-endpoint", "", "Custom AWS KMS endpoint URL",
			withEnv("CLOUDSTIC_KMS_ENDPOINT"), withPlaceholder("<url>"),
			withShortUsage("Custom AWS KMS endpoint")),
		boolFlag(&g.prompt, "prompt", false, "Prompt for password interactively (use alongside --encryption-key or --kms-key-arn to add a password layer)", withShortUsage("Prompt for password interactively")),
	}
}

// outputFlagSpecs covers how results and progress are reported.
func outputFlagSpecs(g *globalFlags) []flagSpec {
	return []flagSpec{
		boolFlag(&g.noPrompt, "no-prompt", false, "Disable interactive prompts (for scripts and CI)"),
		boolFlag(&g.verbose, "verbose", false, "Log detailed file-level operations", withShortUsage("Log detailed operations")),
		boolFlag(&g.quiet, "quiet", false, "Suppress progress bars (keeps final summary)", withShortUsage("Suppress progress bars")),
		boolFlag(&g.json, "json", false, "Write command result as JSON to stdout"),
		boolFlag(&g.debug, "debug", false, "Log every store request (network calls, timing, sizes)", withShortUsage("Log every store request")),
	}
}

// flagGroup names a reusable set of global flag specifications.
type flagGroup func(*globalFlags) []flagSpec

// repoCommandGroups is the set every command that opens a repository needs.
var repoCommandGroups = []flagGroup{repoFlagSpecs, storeSFTPFlagSpecs, encryptionFlagSpecs, outputFlagSpecs}

// backupCommandGroups additionally covers reading from an SFTP source.
var backupCommandGroups = append(append([]flagGroup{}, repoCommandGroups...), sourceSFTPFlagSpecs)

// allGlobalGroups is every global group, used where the full surface is
// required (shell completion's global flag list, profile-derived flag values).
var allGlobalGroups = append(append([]flagGroup{}, repoCommandGroups...), sourceSFTPFlagSpecs)

// globalFlagSpecsFor collects the specs for the given groups.
func globalFlagSpecsFor(g *globalFlags, groups []flagGroup) []flagSpec {
	var specs []flagSpec
	for _, group := range groups {
		specs = append(specs, group(g)...)
	}
	return specs
}

// addGlobalFlags registers the given global flag groups on fs and returns the
// destination struct. Commands pass the groups they actually use so that help
// output does not advertise flags the command cannot act on.
func addGlobalFlags(fs *flag.FlagSet, groups []flagGroup) (*globalFlags, []flagSpec) {
	g := &globalFlags{flagSet: fs}
	specs := globalFlagSpecsFor(g, groups)
	bindFlags(fs, specs)
	return g, specs
}

func (g *globalFlags) jsonEnabled() bool {
	return g != nil && g.json
}

func (g *globalFlags) flagProvided(name string) bool {
	provided := false
	g.flagSet.Visit(func(f *flag.Flag) {
		provided = provided || f.Name == name
	})
	return provided
}

// parseFlags parses args into fs, reordering positional arguments after
// flags so that flags can appear anywhere on the command line (e.g.
// "cloudstic restore abc123 -output ./out.zip" works as well as the reverse).
// Using this consistently means every command supports flexible argument ordering.
func parseFlags(b boundFlagSet, args []string) error {
	fs := b.set
	if fs.Lookup("no-prompt") == nil {
		fs.Bool("no-prompt", false, "Disable interactive prompts (for scripts and CI)")
	}
	if hasGlobalFlag(args, "json") && !hasGlobalFlag(args, "h") {
		fs.SetOutput(io.Discard)
	}
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	// Environment values are resolved here, after parsing, so that explicit
	// flags still win and help output never sees them.
	_, err := applyEnvDefaults(fs, b.all())
	return err
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
