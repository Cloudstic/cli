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
	sources                           [maxEnvironmentDestinations]valueSource
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
	if index, ok := environmentFlagIndex(name); ok && g.sources[index] != "" {
		return g.sources[index]
	}
	return flagValueSource(g.flagSet, name)
}

func (g *globalFlags) setValueSource(name string, source valueSource) {
	if index, ok := environmentFlagIndex(name); ok {
		g.sources[index] = source
	}
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
	annotateEnvironmentUsage(fs)
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	return applyEnvironment(fs)
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
