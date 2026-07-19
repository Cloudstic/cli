package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cloudstic/cli/internal/ui"
)

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "1" || v == "true"
}

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

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{flagSet: fs}
	fs.StringVar(&g.store, "store", envDefault("CLOUDSTIC_STORE", "local:./backup_store"), "Storage backend URI: local:<path>, s3:<bucket>[/<prefix>], b2:<bucket>[/<prefix>], sftp://[user@]host[:port]/<path>")
	defaultProfilesPath, err := defaultProfilesPath()
	if err != nil {
		defaultProfilesPath = defaultProfilesFilename
	}
	fs.StringVar(&g.profile, "profile", envDefault("CLOUDSTIC_PROFILE", ""), "Profile name from profiles.yaml")
	fs.StringVar(&g.profilesFile, "profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPath), "Path to profiles YAML file")
	fs.StringVar(&g.s3Endpoint, "s3-endpoint", envDefault("CLOUDSTIC_S3_ENDPOINT", ""), "S3 compatible endpoint URL (for MinIO, R2, etc.)")
	fs.StringVar(&g.s3Region, "s3-region", envDefault("CLOUDSTIC_S3_REGION", "us-east-1"), "S3 region")
	fs.StringVar(&g.s3Profile, "s3-profile", envDefault("CLOUDSTIC_S3_PROFILE", envDefault("AWS_PROFILE", "")), "AWS shared config profile for S3 credentials")
	fs.StringVar(&g.s3AccessKey, "s3-access-key", envDefault("AWS_ACCESS_KEY_ID", ""), "S3 access key ID")
	fs.StringVar(&g.s3SecretKey, "s3-secret-key", envDefault("AWS_SECRET_ACCESS_KEY", ""), "S3 secret access key")

	fs.StringVar(&g.sourceSFTPPassword, "source-sftp-password", envDefault("CLOUDSTIC_SOURCE_SFTP_PASSWORD", ""), "SFTP source password")
	fs.StringVar(&g.sourceSFTPKey, "source-sftp-key", envDefault("CLOUDSTIC_SOURCE_SFTP_KEY", ""), "Path to SSH private key for SFTP source")
	fs.BoolVar(&g.sourceSFTPInsecure, "source-sftp-insecure", envBool("CLOUDSTIC_SOURCE_SFTP_INSECURE"), "Skip host key validation for SFTP source (INSECURE)")
	fs.StringVar(&g.sourceSFTPKnownHosts, "source-sftp-known-hosts", envDefault("CLOUDSTIC_SOURCE_SFTP_KNOWN_HOSTS", ""), "Path to known_hosts file for SFTP source")

	fs.StringVar(&g.storeSFTPPassword, "store-sftp-password", envDefault("CLOUDSTIC_STORE_SFTP_PASSWORD", ""), "SFTP store password")
	fs.StringVar(&g.storeSFTPKey, "store-sftp-key", envDefault("CLOUDSTIC_STORE_SFTP_KEY", ""), "Path to SSH private key for SFTP store")
	fs.BoolVar(&g.storeSFTPInsecure, "store-sftp-insecure", envBool("CLOUDSTIC_STORE_SFTP_INSECURE"), "Skip host key validation for SFTP store (INSECURE)")
	fs.StringVar(&g.storeSFTPKnownHosts, "store-sftp-known-hosts", envDefault("CLOUDSTIC_STORE_SFTP_KNOWN_HOSTS", ""), "Path to known_hosts file for SFTP store")

	fs.StringVar(&g.encryptionKey, "encryption-key", envDefault("CLOUDSTIC_ENCRYPTION_KEY", ""), "Platform key (hex-encoded, 32 bytes)")
	fs.StringVar(&g.password, "password", envDefault("CLOUDSTIC_PASSWORD", ""), "Repository password")
	fs.StringVar(&g.recoveryKey, "recovery-key", envDefault("CLOUDSTIC_RECOVERY_KEY", ""), "Recovery key (BIP39 24-word mnemonic)")
	fs.StringVar(&g.kmsKeyARN, "kms-key-arn", envDefault("CLOUDSTIC_KMS_KEY_ARN", ""), "AWS KMS key ARN for kms-platform slots")
	fs.StringVar(&g.kmsRegion, "kms-region", envDefault("CLOUDSTIC_KMS_REGION", ""), "AWS KMS region (defaults to us-east-1)")
	fs.StringVar(&g.kmsEndpoint, "kms-endpoint", envDefault("CLOUDSTIC_KMS_ENDPOINT", ""), "Custom AWS KMS endpoint URL")
	fs.BoolVar(&g.disablePackfile, "disable-packfile", envBool("CLOUDSTIC_DISABLE_PACKFILE"), "Disable bundling small objects into 8MB packs")
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
func parseFlags(fs *flag.FlagSet, args []string) error {
	if fs.Lookup("no-prompt") == nil {
		fs.Bool("no-prompt", false, "Disable interactive prompts (for scripts and CI)")
	}
	if hasGlobalFlag(args, "json") && !hasGlobalFlag(args, "h") {
		fs.SetOutput(io.Discard)
	}
	return fs.Parse(reorderArgs(fs, args))
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
