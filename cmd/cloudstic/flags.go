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

type globalFlags struct {
	storeType, storePath, storePrefix                 *string
	s3Endpoint, s3Region                              *string
	s3AccessKey, s3SecretKey                          *string
	sftpHost, sftpPort                                *string
	sftpUser, sftpPassword, sftpKey                   *string
	sourceSFTPHost, sourceSFTPPort                    *string
	sourceSFTPUser, sourceSFTPPassword, sourceSFTPKey *string
	storeSFTPHost, storeSFTPPort                      *string
	storeSFTPUser, storeSFTPPassword, storeSFTPKey    *string
	encryptionKey, encryptionPassword                 *string
	recoveryKey                                       *string
	kmsKeyARN, kmsRegion, kmsEndpoint                 *string
	enablePackfile                                    *bool
	verbose, quiet, debug                             *bool
	debugLog                                          *ui.SafeLogWriter
}

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	g.storeType = fs.String("store", envDefault("CLOUDSTIC_STORE", "local"), "store type (local, b2, s3, sftp)")
	g.storePath = fs.String("store-path", envDefault("CLOUDSTIC_STORE_PATH", "./backup_store"), "Local/SFTP path or B2/S3 bucket name")
	g.storePrefix = fs.String("store-prefix", envDefault("CLOUDSTIC_STORE_PREFIX", ""), "Key prefix for B2/S3 objects")
	g.s3Endpoint = fs.String("s3-endpoint", envDefault("CLOUDSTIC_S3_ENDPOINT", ""), "S3 compatible endpoint URL")
	g.s3Region = fs.String("s3-region", envDefault("CLOUDSTIC_S3_REGION", "us-east-1"), "S3 region")
	g.s3AccessKey = fs.String("s3-access-key", envDefault("AWS_ACCESS_KEY_ID", ""), "S3 access key ID")
	g.s3SecretKey = fs.String("s3-secret-key", envDefault("AWS_SECRET_ACCESS_KEY", ""), "S3 secret access key")
	g.sftpHost = fs.String("sftp-host", envDefault("CLOUDSTIC_SFTP_HOST", ""), "SFTP server hostname")
	g.sftpPort = fs.String("sftp-port", envDefault("CLOUDSTIC_SFTP_PORT", "22"), "SFTP server port")
	g.sftpUser = fs.String("sftp-user", envDefault("CLOUDSTIC_SFTP_USER", ""), "SFTP username")
	g.sftpPassword = fs.String("sftp-password", envDefault("CLOUDSTIC_SFTP_PASSWORD", ""), "SFTP password")
	g.sftpKey = fs.String("sftp-key", envDefault("CLOUDSTIC_SFTP_KEY", ""), "Path to SSH private key")

	g.sourceSFTPHost = fs.String("source-sftp-host", "", "Override: SFTP source hostname")
	g.sourceSFTPPort = fs.String("source-sftp-port", "", "Override: SFTP source port")
	g.sourceSFTPUser = fs.String("source-sftp-user", "", "Override: SFTP source username")
	g.sourceSFTPPassword = fs.String("source-sftp-password", "", "Override: SFTP source password")
	g.sourceSFTPKey = fs.String("source-sftp-key", "", "Override: SFTP source private key")

	g.storeSFTPHost = fs.String("store-sftp-host", "", "Override: SFTP store hostname")
	g.storeSFTPPort = fs.String("store-sftp-port", "", "Override: SFTP store port")
	g.storeSFTPUser = fs.String("store-sftp-user", "", "Override: SFTP store username")
	g.storeSFTPPassword = fs.String("store-sftp-password", "", "Override: SFTP store password")
	g.storeSFTPKey = fs.String("store-sftp-key", "", "Override: SFTP store private key")
	g.encryptionKey = fs.String("encryption-key", envDefault("CLOUDSTIC_ENCRYPTION_KEY", ""), "Platform key (hex-encoded, 32 bytes)")
	g.encryptionPassword = fs.String("encryption-password", envDefault("CLOUDSTIC_ENCRYPTION_PASSWORD", ""), "Password for password-based encryption")
	g.recoveryKey = fs.String("recovery-key", envDefault("CLOUDSTIC_RECOVERY_KEY", ""), "Recovery key (BIP39 24-word mnemonic)")
	g.kmsKeyARN = fs.String("kms-key-arn", envDefault("CLOUDSTIC_KMS_KEY_ARN", ""), "AWS KMS key ARN for kms-platform slots")
	g.kmsRegion = fs.String("kms-region", envDefault("CLOUDSTIC_KMS_REGION", ""), "AWS KMS region (defaults to us-east-1)")
	g.kmsEndpoint = fs.String("kms-endpoint", envDefault("CLOUDSTIC_KMS_ENDPOINT", ""), "Custom AWS KMS endpoint URL")
	g.enablePackfile = fs.Bool("enable-packfile", true, "Bundle small objects into 8MB packs to save S3 PUTs")
	g.verbose = fs.Bool("verbose", false, "Log detailed file-level operations")
	g.quiet = fs.Bool("quiet", false, "Suppress progress bars (keeps final summary)")
	g.debug = fs.Bool("debug", false, "Log every store request (network calls, timing, sizes)")
	return g
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
