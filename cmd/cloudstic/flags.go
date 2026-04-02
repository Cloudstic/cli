package main

import (
	"flag"
	"fmt"
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
	store                             *string
	profile, profilesFile             *string
	s3Endpoint, s3Region, s3Profile   *string
	s3AccessKey, s3SecretKey          *string
	sourceSFTPPassword, sourceSFTPKey *string
	sourceSFTPInsecure                *bool
	sourceSFTPKnownHosts              *string
	storeSFTPPassword, storeSFTPKey   *string
	storeSFTPInsecure                 *bool
	storeSFTPKnownHosts               *string
	encryptionKey                     *string
	password                          *string
	recoveryKey                       *string
	kmsKeyARN, kmsRegion, kmsEndpoint *string
	disablePackfile                   *bool
	prompt, verbose, quiet, debug     *bool
	json                              *bool
	debugLog                          *ui.SafeLogWriter
}

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	g.store = fs.String("store", envDefault("CLOUDSTIC_STORE", "local:./backup_store"), "Storage backend URI: local:<path>, s3:<bucket>[/<prefix>], b2:<bucket>[/<prefix>], sftp://[user@]host[:port]/<path>")
	defaultProfilesPath, err := defaultProfilesPath()
	if err != nil {
		defaultProfilesPath = defaultProfilesFilename
	}
	g.profile = fs.String("profile", envDefault("CLOUDSTIC_PROFILE", ""), "Profile name from profiles.yaml")
	g.profilesFile = fs.String("profiles-file", envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPath), "Path to profiles YAML file")
	g.s3Endpoint = fs.String("s3-endpoint", envDefault("CLOUDSTIC_S3_ENDPOINT", ""), "S3 compatible endpoint URL (for MinIO, R2, etc.)")
	g.s3Region = fs.String("s3-region", envDefault("CLOUDSTIC_S3_REGION", "us-east-1"), "S3 region")
	g.s3Profile = fs.String("s3-profile", envDefault("CLOUDSTIC_S3_PROFILE", envDefault("AWS_PROFILE", "")), "AWS shared config profile for S3 credentials")
	g.s3AccessKey = fs.String("s3-access-key", envDefault("AWS_ACCESS_KEY_ID", ""), "S3 access key ID")
	g.s3SecretKey = fs.String("s3-secret-key", envDefault("AWS_SECRET_ACCESS_KEY", ""), "S3 secret access key")

	g.sourceSFTPPassword = fs.String("source-sftp-password", envDefault("CLOUDSTIC_SOURCE_SFTP_PASSWORD", ""), "SFTP source password")
	g.sourceSFTPKey = fs.String("source-sftp-key", envDefault("CLOUDSTIC_SOURCE_SFTP_KEY", ""), "Path to SSH private key for SFTP source")
	g.sourceSFTPInsecure = fs.Bool("source-sftp-insecure", envBool("CLOUDSTIC_SOURCE_SFTP_INSECURE"), "Skip host key validation for SFTP source (INSECURE)")
	g.sourceSFTPKnownHosts = fs.String("source-sftp-known-hosts", envDefault("CLOUDSTIC_SOURCE_SFTP_KNOWN_HOSTS", ""), "Path to known_hosts file for SFTP source")

	g.storeSFTPPassword = fs.String("store-sftp-password", envDefault("CLOUDSTIC_STORE_SFTP_PASSWORD", ""), "SFTP store password")
	g.storeSFTPKey = fs.String("store-sftp-key", envDefault("CLOUDSTIC_STORE_SFTP_KEY", ""), "Path to SSH private key for SFTP store")
	g.storeSFTPInsecure = fs.Bool("store-sftp-insecure", envBool("CLOUDSTIC_STORE_SFTP_INSECURE"), "Skip host key validation for SFTP store (INSECURE)")
	g.storeSFTPKnownHosts = fs.String("store-sftp-known-hosts", envDefault("CLOUDSTIC_STORE_SFTP_KNOWN_HOSTS", ""), "Path to known_hosts file for SFTP store")

	g.encryptionKey = fs.String("encryption-key", envDefault("CLOUDSTIC_ENCRYPTION_KEY", ""), "Platform key (hex-encoded, 32 bytes)")
	g.password = fs.String("password", envDefault("CLOUDSTIC_PASSWORD", ""), "Repository password")
	g.recoveryKey = fs.String("recovery-key", envDefault("CLOUDSTIC_RECOVERY_KEY", ""), "Recovery key (BIP39 24-word mnemonic)")
	g.kmsKeyARN = fs.String("kms-key-arn", envDefault("CLOUDSTIC_KMS_KEY_ARN", ""), "AWS KMS key ARN for kms-platform slots")
	g.kmsRegion = fs.String("kms-region", envDefault("CLOUDSTIC_KMS_REGION", ""), "AWS KMS region (defaults to us-east-1)")
	g.kmsEndpoint = fs.String("kms-endpoint", envDefault("CLOUDSTIC_KMS_ENDPOINT", ""), "Custom AWS KMS endpoint URL")
	g.disablePackfile = fs.Bool("disable-packfile", envBool("CLOUDSTIC_DISABLE_PACKFILE"), "Disable bundling small objects into 8MB packs")
	g.prompt = fs.Bool("prompt", false, "Prompt for password interactively (use alongside --encryption-key or --kms-key-arn to add a password layer)")
	g.verbose = fs.Bool("verbose", false, "Log detailed file-level operations")
	g.quiet = fs.Bool("quiet", false, "Suppress progress bars (keeps final summary)")
	g.json = fs.Bool("json", false, "Write command result as JSON to stdout")
	g.debug = fs.Bool("debug", false, "Log every store request (network calls, timing, sizes)")
	return g
}

func (g *globalFlags) jsonEnabled() bool {
	return g != nil && g.json != nil && *g.json
}

func cliFlagProvided(name string) bool {
	for _, arg := range os.Args[1:] {
		if arg == "-"+name || arg == "--"+name {
			return true
		}
		if strings.HasPrefix(arg, "-"+name+"=") || strings.HasPrefix(arg, "--"+name+"=") {
			return true
		}
	}
	return false
}

// mustParse parses os.Args[2:] into fs, reordering positional arguments after
// flags so that flags can appear anywhere on the command line (e.g.
// "cloudstic restore abc123 -output ./out.zip" works as well as the reverse).
// Using this consistently means every command supports flexible argument ordering.
func mustParse(fs *flag.FlagSet) {
	_ = fs.Parse(reorderArgs(fs, os.Args[2:]))
}

// reorderArgs moves flag arguments before positional arguments so that Go's
// flag package (which stops at the first non-flag) parses all flags regardless
// of where they appear on the command line.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
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
