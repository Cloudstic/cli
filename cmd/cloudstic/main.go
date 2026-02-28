package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/paths"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jedib0t/go-pretty/v6/list"
	"github.com/jedib0t/go-pretty/v6/table"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("cloudstic %s (commit %s, built %s)\n", version, commit, date)
		return
	case "init":
		runInit()
	case "backup":
		runBackup()
	case "restore":
		runRestore()
	case "list":
		runList()
	case "ls":
		runLsSnapshot()
	case "prune":
		runPrune()
	case "forget":
		runForget()
	case "diff":
		runDiff()
	case "break-lock":
		runBreakLock()
	case "add-recovery-key":
		runAddRecoveryKey()
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	t := ui.NewTermWriter(os.Stdout)

	_, _ = fmt.Fprintf(t.W, "%sCloudstic%s — Content-Addressable Backup System\n", ui.Bold, ui.Reset)

	t.Heading("USAGE")
	_, _ = fmt.Fprintf(t.W, "  cloudstic %s<command>%s [options]\n", ui.Cyan, ui.Reset)

	t.Heading("COMMANDS")
	t.Commands([][2]string{
		{"init", "Initialize a new repository (must run before first backup)"},
		{"backup", "Create a new backup snapshot from a source"},
		{"restore", "Restore files from a backup snapshot"},
		{"list", "List all backup snapshots in the repository"},
		{"ls", "List files within a specific snapshot"},
		{"prune", "Remove unused data chunks from the repository"},
		{"forget", "Remove a specific snapshot from history"},
		{"diff", "Compare two snapshots or a snapshot against latest"},
		{"break-lock", "Remove a stale repository lock left by a crashed process"},
		{"add-recovery-key", "Generate a recovery key for an existing encrypted repository"},
	})

	t.HeadingSub("GLOBAL OPTIONS", "(also settable via env vars)")
	t.Flags([][2]string{
		{"-store <type>", ui.Env("Storage backend: local, b2, s3, sftp, hybrid", "CLOUDSTIC_STORE")},
		{"-store-path <path>", ui.Env("Local/SFTP path or B2/S3 bucket name", "CLOUDSTIC_STORE_PATH")},
		{"-store-prefix <pfx>", ui.Env("Key prefix for B2/S3 objects", "CLOUDSTIC_STORE_PREFIX")},
		{"-s3-endpoint <url>", ui.Env("S3 compatible endpoint (for MinIO, R2, etc.)", "CLOUDSTIC_S3_ENDPOINT")},
		{"-s3-region <region>", ui.Env("S3 region", "CLOUDSTIC_S3_REGION")},
		{"-s3-access-key <key>", ui.Env("S3 Access Key ID", "AWS_ACCESS_KEY_ID")},
		{"-s3-secret-key <secret>", ui.Env("S3 Secret Access Key", "AWS_SECRET_ACCESS_KEY")},
		{"-sftp-host <host>", ui.Env("SFTP server hostname", "CLOUDSTIC_SFTP_HOST")},
		{"-sftp-port <port>", ui.Env("SFTP server port (default 22)", "CLOUDSTIC_SFTP_PORT")},
		{"-sftp-user <user>", ui.Env("SFTP username", "CLOUDSTIC_SFTP_USER")},
		{"-sftp-password <pw>", ui.Env("SFTP password", "CLOUDSTIC_SFTP_PASSWORD")},
		{"-sftp-key <path>", ui.Env("Path to SSH private key", "CLOUDSTIC_SFTP_KEY")},
		{"-database-url <url>", ui.Env("PostgreSQL URL (hybrid store)", "CLOUDSTIC_DATABASE_URL")},
		{"-tenant-id <uuid>", ui.Env("Tenant ID for hybrid store RLS", "CLOUDSTIC_TENANT_ID")},
		{"-verbose", "Log detailed file-level operations"},
		{"-quiet", "Suppress progress bars (keeps final summary)"},
		{"-debug", "Log every store request (network calls, timing, sizes)"},
	})

	t.Heading("ENCRYPTION OPTIONS")
	t.Flags([][2]string{
		{"-encryption-key <hex>", ui.Env("Platform key (64 hex chars = 32 bytes)", "CLOUDSTIC_ENCRYPTION_KEY")},
		{"-encryption-password", ui.Env("Password for password-based encryption", "CLOUDSTIC_ENCRYPTION_PASSWORD")},
		{"-recovery-key <words>", ui.Env("Recovery key (24-word seed phrase)", "CLOUDSTIC_RECOVERY_KEY")},
	})
	t.Blank()
	t.Note(
		"Encryption is required by default (AES-256-GCM). Provide -encryption-password",
		"or -encryption-key when running 'cloudstic init'. Use -recovery-key to open a",
		"repository with a recovery seed phrase.",
	)

	t.Heading("COMMAND DETAILS")

	t.Command("init", "")
	t.Flags([][2]string{
		{"-recovery", "Generate a 24-word recovery key during init"},
		{"-no-encryption", "Create an unencrypted repository (not recommended)"},
	})
	t.Blank()

	t.Command("add-recovery-key", "")
	t.Note(
		"  Generate a 24-word recovery key for an existing encrypted repository.",
		"  Requires -encryption-key or -encryption-password to unlock the master key.",
	)
	t.Blank()

	t.Command("backup", "")
	t.Flags([][2]string{
		{"-source <type>", "local, sftp, gdrive, gdrive-changes, onedrive, onedrive-changes"},
		{"-source-path <path>", "Path to source directory (local or SFTP remote path)"},
		{"-drive-id <id>", "Shared drive ID for gdrive (omit for My Drive)"},
		{"-root-folder <id>", "Root folder ID for gdrive (defaults to entire drive)"},
		{"-tag <tag>", "Tag to apply to the snapshot (repeatable)"},
		{"-dry-run", "Scan source and report changes without writing to the store"},
	})
	t.Blank()

	t.Command("restore", "[snapshot_id]")
	t.Flags([][2]string{
		{"-output <path>", "Output ZIP file path (default: ./restore.zip)"},
		{"-dry-run", "Show what would be restored without writing the archive"},
	})
	t.Blank()

	t.Command("list", "")
	t.Note("  No additional flags.")
	t.Blank()

	t.Command("ls", "[snapshot_id]")
	t.Note("  List files in the specified snapshot (defaults to latest).")
	t.Blank()

	t.Command("prune", "")
	t.Flags([][2]string{
		{"-dry-run", "Show what would be deleted without deleting"},
	})
	t.Blank()

	t.Command("forget", "<snapshot_id>")
	t.Flags([][2]string{
		{"-prune", "Run prune immediately after forgetting"},
		{"-dry-run", "Show what would be removed without acting"},
		{"-keep-last N", "Keep the N most recent snapshots"},
		{"-keep-daily N", "Keep N daily snapshots"},
		{"-keep-weekly N", "Keep N weekly snapshots"},
		{"-keep-monthly N", "Keep N monthly snapshots"},
		{"-keep-yearly N", "Keep N yearly snapshots"},
	})
	t.Blank()

	t.Command("diff", "<snapshot_1> <snapshot_2>")
	t.Note("  Compare two snapshots. Use 'latest' as an alias for the most recent.")
	t.Blank()

	t.Command("break-lock", "")
	t.Note("  Remove a stale repository lock left by a crashed or killed process.",
		"  Only use this if you are sure no other operation is running.")

	t.Heading("EXAMPLES")
	t.Examples(
		`cloudstic init -encryption-password "my secret passphrase"`,
		`cloudstic init -encryption-password "my secret passphrase" -recovery`,
		"cloudstic backup -source local -source-path ./documents",
		"cloudstic backup -source gdrive -store b2 -store-path my-bucket",
		"cloudstic list",
		"cloudstic restore",
		"cloudstic restore abc123 -output ./my-backup.zip",
		"cloudstic backup -source local -source-path ./documents -dry-run",
		"cloudstic prune -dry-run -verbose",
	)
	t.Blank()
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type globalFlags struct {
	storeType, storePath, storePrefix *string
	s3Endpoint, s3Region              *string
	s3AccessKey, s3SecretKey          *string
	sftpHost, sftpPort                *string
	sftpUser, sftpPassword, sftpKey   *string
	databaseURL, tenantID             *string
	encryptionKey, encryptionPassword *string
	recoveryKey                       *string
	verbose, quiet, debug             *bool
	debugLog                          *ui.SafeLogWriter
}

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	g.storeType = fs.String("store", envDefault("CLOUDSTIC_STORE", "local"), "store type (local, b2, s3, sftp, hybrid)")
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
	g.databaseURL = fs.String("database-url", envDefault("CLOUDSTIC_DATABASE_URL", ""), "PostgreSQL URL (required for hybrid store)")
	g.tenantID = fs.String("tenant-id", envDefault("CLOUDSTIC_TENANT_ID", ""), "Tenant ID for hybrid store RLS")
	g.encryptionKey = fs.String("encryption-key", envDefault("CLOUDSTIC_ENCRYPTION_KEY", ""), "Platform key (hex-encoded, 32 bytes)")
	g.encryptionPassword = fs.String("encryption-password", envDefault("CLOUDSTIC_ENCRYPTION_PASSWORD", ""), "Password for password-based encryption")
	g.recoveryKey = fs.String("recovery-key", envDefault("CLOUDSTIC_RECOVERY_KEY", ""), "Recovery key (BIP39 24-word mnemonic)")
	g.verbose = fs.Bool("verbose", false, "Log detailed file-level operations")
	g.quiet = fs.Bool("quiet", false, "Suppress progress bars (keeps final summary)")
	g.debug = fs.Bool("debug", false, "Log every store request (network calls, timing, sizes)")
	return g
}

const configKey = "config"

func (g *globalFlags) openStore() (store.ObjectStore, []byte, error) {
	raw, err := g.initObjectStore()
	if err != nil {
		return nil, nil, err
	}
	if *g.debug {
		if g.debugLog == nil {
			g.debugLog = &ui.SafeLogWriter{}
		}
		raw = store.NewDebugStore(raw, g.debugLog)
	}

	cfg, err := loadRepoConfig(raw)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("repository not initialized -- run 'cloudstic init' first")
	}

	if !cfg.Encrypted {
		return raw, nil, nil
	}

	platformKey, err := g.parsePlatformKey()
	if err != nil {
		return nil, nil, err
	}
	if len(platformKey) == 0 && *g.encryptionPassword == "" && *g.recoveryKey == "" {
		return nil, nil, fmt.Errorf("repository is encrypted -- provide --encryption-key, --encryption-password, or --recovery-key")
	}

	slots, err := g.loadKeySlots(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("load encryption key slots: %w", err)
	}
	if len(slots) == 0 {
		return nil, nil, fmt.Errorf("repository is encrypted but no key slots found")
	}

	encKey, err := openExistingSlots(slots, platformKey, *g.encryptionPassword, *g.recoveryKey)
	if err != nil {
		return nil, nil, err
	}
	return raw, encKey, nil
}

func (g *globalFlags) openClient() (*cloudstic.Client, error) {
	raw, encKey, err := g.openStore()
	if err != nil {
		return nil, err
	}
	if g.debugLog != nil {
		engine.DebugWriter = g.debugLog
	}
	var reporter cloudstic.Reporter
	if *g.quiet {
		reporter = ui.NewNoOpReporter()
	} else {
		cr := ui.NewConsoleReporter()
		if g.debugLog != nil {
			cr.SetLogWriter(g.debugLog)
		}
		reporter = cr
	}
	return cloudstic.NewClient(raw,
		cloudstic.WithEncryptionKey(encKey),
		cloudstic.WithReporter(reporter),
	)
}

func loadRepoConfig(s store.ObjectStore) (*core.RepoConfig, error) {
	data, err := s.Get(context.Background(), configKey)
	if err != nil {
		return nil, nil
	}
	var cfg core.RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse repo config: %w", err)
	}
	return &cfg, nil
}

func writeRepoConfig(s store.ObjectStore, encrypted bool) error {
	cfg := core.RepoConfig{
		Version:   1,
		Created:   time.Now().UTC().Format(time.RFC3339),
		Encrypted: encrypted,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal repo config: %w", err)
	}
	return s.Put(context.Background(), configKey, data)
}

func (g *globalFlags) parsePlatformKey() ([]byte, error) {
	encKeyHex := *g.encryptionKey
	if encKeyHex == "" {
		return nil, nil
	}
	platformKey, err := hex.DecodeString(encKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid --encryption-key (must be hex-encoded): %w", err)
	}
	if len(platformKey) != crypto.KeySize {
		return nil, fmt.Errorf("--encryption-key must be %d bytes (%d hex chars), got %d bytes", crypto.KeySize, crypto.KeySize*2, len(platformKey))
	}
	return platformKey, nil
}

func (g *globalFlags) loadKeySlots(rawStore store.ObjectStore) ([]store.KeySlot, error) {
	if hybrid, ok := rawStore.(*store.HybridStore); ok {
		slots, err := store.LoadKeySlotsFromDB(hybrid.DB())
		if err == nil && len(slots) > 0 {
			store.SyncKeySlots(hybrid.B2(), slots)
			return slots, nil
		}
		slots, err = store.LoadKeySlots(hybrid.B2())
		if err == nil && len(slots) > 0 {
			return slots, nil
		}
	}
	return store.LoadKeySlots(rawStore)
}

func openExistingSlots(slots []store.KeySlot, platformKey []byte, password, recoveryMnemonic string) ([]byte, error) {
	if len(platformKey) > 0 {
		if key, err := store.OpenWithPlatformKey(slots, platformKey); err == nil {
			return key, nil
		}
	}
	if password != "" {
		if key, err := store.OpenWithPassword(slots, password); err == nil {
			return key, nil
		}
	}
	if recoveryMnemonic != "" {
		recoveryKey, err := crypto.MnemonicToKey(recoveryMnemonic)
		if err != nil {
			return nil, fmt.Errorf("invalid recovery key mnemonic: %w", err)
		}
		if key, err := store.OpenWithRecoveryKey(slots, recoveryKey); err == nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("could not open repository: no provided credential matches the stored key slots (types: %s)", store.SlotTypes(slots))
}

// runInit bootstraps a new repository: creates encryption key slots and
// writes the "config" marker. Encryption is required by default; pass
// --no-encryption to explicitly create an unencrypted repository.
func runInit() {
	initCmd := flag.NewFlagSet("init", flag.ExitOnError)
	g := addGlobalFlags(initCmd)
	recovery := initCmd.Bool("recovery", false, "Generate a recovery key (24-word seed phrase) during init")
	noEncryption := initCmd.Bool("no-encryption", false, "Create an unencrypted repository (NOT recommended)")
	_ = initCmd.Parse(os.Args[2:])

	raw, err := g.initObjectStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}

	cfg, err := loadRepoConfig(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read config: %v\n", err)
		os.Exit(1)
	}
	if cfg != nil {
		fmt.Fprintln(os.Stderr, "Repository is already initialized.")
		os.Exit(1)
	}

	platformKey, err := g.parsePlatformKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	password := *g.encryptionPassword
	hasEncryptionCreds := len(platformKey) > 0 || password != ""

	if !hasEncryptionCreds && !*noEncryption {
		fmt.Fprintln(os.Stderr, "Error: encryption is required by default.")
		fmt.Fprintln(os.Stderr, "Provide --encryption-password or --encryption-key to encrypt your repository.")
		fmt.Fprintln(os.Stderr, "To create an unencrypted repository, pass --no-encryption (not recommended).")
		os.Exit(1)
	}

	encrypted := hasEncryptionCreds

	if encrypted {
		slots, err := g.loadKeySlots(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load key slots: %v\n", err)
			os.Exit(1)
		}

		if len(slots) > 0 {
			if _, err := openExistingSlots(slots, platformKey, password, ""); err != nil {
				fmt.Fprintf(os.Stderr, "Found existing key slots but cannot open them: %v\n", err)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "Adopted existing encryption key slots.")
		} else {
			if _, err := store.InitEncryptionKey(raw, platformKey, password); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to initialize encryption: %v\n", err)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "Created new encryption key slots.")
		}

		if *recovery {
			slots, err := g.loadKeySlots(raw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to reload key slots: %v\n", err)
				os.Exit(1)
			}
			masterKey, err := store.ExtractMasterKey(slots, platformKey, password)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to extract master key for recovery slot: %v\n", err)
				os.Exit(1)
			}
			mnemonic, err := store.AddRecoverySlot(raw, masterKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create recovery key: %v\n", err)
				os.Exit(1)
			}
			printRecoveryKey(mnemonic)
		}
	} else {
		fmt.Fprintln(os.Stderr, "WARNING: creating unencrypted repository. Your backups will NOT be encrypted at rest.")
	}

	if err := writeRepoConfig(raw, encrypted); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write config: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Repository initialized (encrypted: %v).\n", encrypted)
}

func printRecoveryKey(mnemonic string) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║                      RECOVERY KEY                           ║")
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════╣")
	fmt.Fprintln(os.Stderr, "║                                                              ║")
	fmt.Fprintf(os.Stderr, "║  %s\n", mnemonic)
	fmt.Fprintln(os.Stderr, "║                                                              ║")
	fmt.Fprintln(os.Stderr, "║  Write down these 24 words and store them in a safe place.   ║")
	fmt.Fprintln(os.Stderr, "║  This is the ONLY time the recovery key will be displayed.   ║")
	fmt.Fprintln(os.Stderr, "║  If you lose your password, this key is your only way to     ║")
	fmt.Fprintln(os.Stderr, "║  recover your encrypted backups.                             ║")
	fmt.Fprintln(os.Stderr, "║                                                              ║")
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr)
}

func runAddRecoveryKey() {
	addCmd := flag.NewFlagSet("add-recovery-key", flag.ExitOnError)
	g := addGlobalFlags(addCmd)
	_ = addCmd.Parse(os.Args[2:])

	raw, err := g.initObjectStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}

	cfg, err := loadRepoConfig(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read config: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "Repository not initialized -- run 'cloudstic init' first.")
		os.Exit(1)
	}
	if !cfg.Encrypted {
		fmt.Fprintln(os.Stderr, "Repository is not encrypted -- recovery keys are only for encrypted repositories.")
		os.Exit(1)
	}

	platformKey, err := g.parsePlatformKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	password := *g.encryptionPassword
	if len(platformKey) == 0 && password == "" {
		fmt.Fprintln(os.Stderr, "Provide --encryption-key or --encryption-password to unlock the master key.")
		os.Exit(1)
	}

	slots, err := g.loadKeySlots(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load key slots: %v\n", err)
		os.Exit(1)
	}

	masterKey, err := store.ExtractMasterKey(slots, platformKey, password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to extract master key: %v\n", err)
		os.Exit(1)
	}

	mnemonic, err := store.AddRecoverySlot(raw, masterKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create recovery key: %v\n", err)
		os.Exit(1)
	}

	printRecoveryKey(mnemonic)
	fmt.Fprintln(os.Stderr, "Recovery key slot has been added to the repository.")
}

func (g *globalFlags) initObjectStore() (store.ObjectStore, error) {
	switch *g.storeType {
	case "local":
		return store.NewLocalStore(*g.storePath)
	case "b2":
		keyID := os.Getenv("B2_KEY_ID")
		appKey := os.Getenv("B2_APP_KEY")
		if keyID == "" || appKey == "" {
			return nil, fmt.Errorf("B2_KEY_ID and B2_APP_KEY env vars required for b2 store")
		}
		return store.NewB2StoreWithPrefix(keyID, appKey, *g.storePath, *g.storePrefix)
	case "s3":
		if *g.storePath == "" {
			return nil, fmt.Errorf("-store-path must be set to the S3 bucket name")
		}
		return store.NewS3Store(context.Background(), *g.s3Endpoint, *g.s3Region, *g.storePath, *g.s3AccessKey, *g.s3SecretKey, *g.storePrefix)
	case "sftp":
		cfg, err := g.sftpConfig()
		if err != nil {
			return nil, err
		}
		return store.NewSFTPStore(cfg, *g.storePath)
	case "hybrid":
		return g.initHybridStore()
	default:
		return nil, fmt.Errorf("unsupported store type: %s", *g.storeType)
	}
}

func (g *globalFlags) sftpConfig() (store.SFTPConfig, error) {
	if *g.sftpHost == "" {
		return store.SFTPConfig{}, fmt.Errorf("--sftp-host (or CLOUDSTIC_SFTP_HOST) is required for sftp store/source")
	}
	if *g.sftpUser == "" {
		return store.SFTPConfig{}, fmt.Errorf("--sftp-user (or CLOUDSTIC_SFTP_USER) is required for sftp store/source")
	}
	return store.SFTPConfig{
		Host:           *g.sftpHost,
		Port:           *g.sftpPort,
		User:           *g.sftpUser,
		Password:       *g.sftpPassword,
		PrivateKeyPath: *g.sftpKey,
	}, nil
}

func (g *globalFlags) initHybridStore() (store.ObjectStore, error) {
	if *g.databaseURL == "" {
		return nil, fmt.Errorf("--database-url (or CLOUDSTIC_DATABASE_URL) required for hybrid store")
	}
	tenantID := *g.tenantID
	if tenantID == "" {
		tenantID = extractTenantID(*g.storePrefix)
	}
	if tenantID == "" {
		return nil, fmt.Errorf("--tenant-id (or CLOUDSTIC_TENANT_ID) required for hybrid store; or use --store-prefix backups/<uuid>/")
	}

	keyID := os.Getenv("B2_KEY_ID")
	appKey := os.Getenv("B2_APP_KEY")
	if keyID == "" || appKey == "" {
		return nil, fmt.Errorf("B2_KEY_ID and B2_APP_KEY env vars required for hybrid store")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, *g.databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	b2, err := store.NewB2StoreWithPrefix(keyID, appKey, *g.storePath, *g.storePrefix)
	if err != nil {
		pool.Close()
		return nil, err
	}

	txFunc := newTenantTxFunc(pool, tenantID)
	return store.NewHybridStore(txFunc, b2), nil
}

// newTenantTxFunc returns a TxFunc that runs each callback in a PostgreSQL
// transaction with the RLS tenant_id set.
func newTenantTxFunc(pool *pgxpool.Pool, tenantID string) store.TxFunc {
	safe := strings.ReplaceAll(tenantID, "'", "''")
	return func(fn func(ctx context.Context, tx pgx.Tx) error) error {
		ctx := context.Background()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL cloudstic.tenant_id = '%s'", safe)); err != nil {
			return fmt.Errorf("set tenant_id: %w", err)
		}
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
}

// extractTenantID parses a tenant UUID from a store prefix like "backups/<uuid>/".
func extractTenantID(prefix string) string {
	prefix = strings.TrimSuffix(prefix, "/")
	parts := strings.Split(prefix, "/")
	if len(parts) >= 2 && parts[0] == "backups" {
		return parts[1]
	}
	return ""
}

func initSource(sourceType, sourcePath, driveID, rootFolder string, g *globalFlags) (store.Source, error) {
	switch sourceType {
	case "local":
		return store.NewLocalSource(sourcePath), nil
	case "sftp":
		cfg, err := g.sftpConfig()
		if err != nil {
			return nil, err
		}
		if sourcePath == "" {
			return nil, fmt.Errorf("-source-path is required for sftp source")
		}
		return store.NewSFTPSource(cfg, sourcePath)
	case "gdrive":
		creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("GOOGLE_TOKEN_FILE", "google_token.json")
		if err != nil {
			return nil, err
		}
		src, err := store.NewGDriveSource(creds, tokenPath)
		if err != nil {
			return nil, err
		}
		src.DriveID = driveID
		src.RootFolderID = rootFolder
		return src, nil
	case "gdrive-changes":
		creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("GOOGLE_TOKEN_FILE", "google_token.json")
		if err != nil {
			return nil, err
		}
		src, err := store.NewGDriveChangeSource(creds, tokenPath)
		if err != nil {
			return nil, err
		}
		src.DriveID = driveID
		src.RootFolderID = rootFolder
		return src, nil
	case "onedrive":
		clientID := os.Getenv("ONEDRIVE_CLIENT_ID") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("ONEDRIVE_TOKEN_FILE", "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return store.NewOneDriveSource(clientID, tokenPath)
	case "onedrive-changes":
		clientID := os.Getenv("ONEDRIVE_CLIENT_ID") // optional; uses built-in OAuth client when empty
		tokenPath, err := resolveTokenPath("ONEDRIVE_TOKEN_FILE", "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return store.NewOneDriveChangeSource(clientID, tokenPath)
	default:
		return nil, fmt.Errorf("unsupported source type: %s", sourceType)
	}
}

// resolveTokenPath returns an absolute path for a token file. If the
// environment variable envKey is set, that value is used as-is. Otherwise
// the filename is placed inside the cloudstic config directory.
func resolveTokenPath(envKey, defaultFilename string) (string, error) {
	if v := os.Getenv(envKey); v != "" {
		return v, nil
	}
	return paths.TokenPath(defaultFilename)
}

func runDiff() {
	diffCmd := flag.NewFlagSet("diff", flag.ExitOnError)
	g := addGlobalFlags(diffCmd)

	_ = diffCmd.Parse(reorderArgs(diffCmd, os.Args[2:]))

	if diffCmd.NArg() < 2 {
		fmt.Println("Usage: cloudstic diff [options] <snapshot_id1> <snapshot_id2>")
		fmt.Println("       cloudstic diff [options] <snapshot_id1> latest")
		os.Exit(1)
	}
	snap1 := diffCmd.Arg(0)
	snap2 := diffCmd.Arg(1)

	ctx := context.Background()

	client, err := g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}
	result, err := client.Diff(ctx, snap1, snap2)
	if err != nil {
		fmt.Printf("Diff failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Diffing %s vs %s\n", result.Ref1, result.Ref2)
	for _, c := range result.Changes {
		fmt.Printf("%s %s\n", c.Type, c.Path)
	}
}

func runForget() {
	forgetCmd := flag.NewFlagSet("forget", flag.ExitOnError)
	g := addGlobalFlags(forgetCmd)
	prune := forgetCmd.Bool("prune", false, "Run prune after forgetting")
	dryRun := forgetCmd.Bool("dry-run", false, "Only show what would be removed")

	keepLast := forgetCmd.Int("keep-last", 0, "Keep the last n snapshots")
	keepHourly := forgetCmd.Int("keep-hourly", 0, "Keep n hourly snapshots")
	keepDaily := forgetCmd.Int("keep-daily", 0, "Keep n daily snapshots")
	keepWeekly := forgetCmd.Int("keep-weekly", 0, "Keep n weekly snapshots")
	keepMonthly := forgetCmd.Int("keep-monthly", 0, "Keep n monthly snapshots")
	keepYearly := forgetCmd.Int("keep-yearly", 0, "Keep n yearly snapshots")

	var filterTags stringArrayFlags
	forgetCmd.Var(&filterTags, "tag", "Filter by tag (can be specified multiple times)")
	filterSource := forgetCmd.String("source", "", "Filter by source type")
	filterAccount := forgetCmd.String("account", "", "Filter by account")
	filterPath := forgetCmd.String("path", "", "Filter by path")

	groupBy := forgetCmd.String("group-by", "source,account,path", "Group snapshots by fields (comma-separated)")

	_ = forgetCmd.Parse(reorderArgs(forgetCmd, os.Args[2:]))

	hasPolicy := *keepLast > 0 || *keepHourly > 0 || *keepDaily > 0 ||
		*keepWeekly > 0 || *keepMonthly > 0 || *keepYearly > 0
	snapshotID := forgetCmd.Arg(0)

	if snapshotID == "" && !hasPolicy {
		fmt.Println("Usage: cloudstic forget [options] <snapshot_id>")
		fmt.Println("       cloudstic forget --keep-last n [--keep-daily n] [--prune] [--dry-run]")
		os.Exit(1)
	}

	ctx := context.Background()

	client, err := g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	if hasPolicy {
		var opts []cloudstic.ForgetOption
		if *prune {
			opts = append(opts, cloudstic.WithPrune())
		}
		if *dryRun {
			opts = append(opts, cloudstic.WithDryRun())
		}
		if *keepLast > 0 {
			opts = append(opts, cloudstic.WithKeepLast(*keepLast))
		}
		if *keepHourly > 0 {
			opts = append(opts, cloudstic.WithKeepHourly(*keepHourly))
		}
		if *keepDaily > 0 {
			opts = append(opts, cloudstic.WithKeepDaily(*keepDaily))
		}
		if *keepWeekly > 0 {
			opts = append(opts, cloudstic.WithKeepWeekly(*keepWeekly))
		}
		if *keepMonthly > 0 {
			opts = append(opts, cloudstic.WithKeepMonthly(*keepMonthly))
		}
		if *keepYearly > 0 {
			opts = append(opts, cloudstic.WithKeepYearly(*keepYearly))
		}
		for _, tag := range filterTags {
			opts = append(opts, cloudstic.WithFilterTag(tag))
		}
		if *filterSource != "" {
			opts = append(opts, cloudstic.WithFilterSource(*filterSource))
		}
		if *filterAccount != "" {
			opts = append(opts, cloudstic.WithFilterAccount(*filterAccount))
		}
		if *filterPath != "" {
			opts = append(opts, cloudstic.WithFilterPath(*filterPath))
		}
		opts = append(opts, cloudstic.WithGroupBy(*groupBy))

		result, err := client.ForgetPolicy(ctx, opts...)
		if err != nil {
			fmt.Printf("Forget failed: %v\n", err)
			os.Exit(1)
		}
		printPolicyResult(result, *dryRun)
		return
	}

	// Single snapshot forget
	var forgetOpts []cloudstic.ForgetOption
	if *prune {
		forgetOpts = append(forgetOpts, cloudstic.WithPrune())
	}
	result, err := client.Forget(ctx, snapshotID, forgetOpts...)
	if err != nil {
		fmt.Printf("Forget failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Snapshot removed.")
	if result.Prune != nil {
		printPruneStats(result.Prune)
	}
}

func printPolicyResult(result *cloudstic.PolicyResult, dryRun bool) {
	for _, group := range result.Groups {
		fmt.Printf("\nSnapshots for %s:\n", group.Key)

		if len(group.Keep) > 0 {
			fmt.Printf("\nkeep %d snapshots:\n", len(group.Keep))
			reasons := make(map[string]string, len(group.Keep))
			entries := make([]engine.SnapshotEntry, 0, len(group.Keep))
			for _, k := range group.Keep {
				entries = append(entries, k.Entry)
				reasons[k.Entry.Ref] = strings.Join(k.Reasons, ", ")
			}
			renderSnapshotTable(entries, reasons)
		}

		if len(group.Remove) > 0 {
			action := "remove"
			if dryRun {
				action = "would remove"
			}
			fmt.Printf("\n%s %d snapshots:\n", action, len(group.Remove))
			renderSnapshotTable(group.Remove, nil)
		}
	}

	totalRemoved := 0
	for _, g := range result.Groups {
		totalRemoved += len(g.Remove)
	}

	fmt.Println()
	if dryRun {
		fmt.Printf("%d snapshots would be removed (dry run)\n", totalRemoved)
	} else if totalRemoved > 0 {
		fmt.Printf("%d snapshots have been removed\n", totalRemoved)
		if result.Prune != nil {
			printPruneStats(result.Prune)
		}
	} else {
		fmt.Println("No snapshots to remove")
	}
}

// renderSnapshotTable prints a table of snapshot entries. If reasons is non-nil,
// a "Reasons" column is appended with the value for each snapshot ref.
func renderSnapshotTable(entries []engine.SnapshotEntry, reasons map[string]string) {
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)

	header := table.Row{"Seq", "Created", "Snapshot Hash", "Source", "Account", "Path", "Tags"}
	if reasons != nil {
		header = append(header, "Reasons")
	}
	t.AppendHeader(header)

	for _, e := range entries {
		var source, account, path string
		if e.Snap.Source != nil {
			source = e.Snap.Source.Type
			account = e.Snap.Source.Account
			path = e.Snap.Source.Path
		} else if e.Snap.Meta != nil {
			source = e.Snap.Meta["source"]
		}

		hash := strings.TrimPrefix(e.Ref, "snapshot/")
		tags := strings.Join(e.Snap.Tags, ", ")

		row := table.Row{e.Snap.Seq, e.Snap.Created, hash, source, account, path, tags}
		if reasons != nil {
			row = append(row, reasons[e.Ref])
		}
		t.AppendRow(row)
	}

	t.Render()
}

func renderSnapshotTree(result *engine.LsSnapshotResult) {
	l := list.NewWriter()
	l.SetOutputMirror(os.Stdout)
	for _, rootRef := range result.RootRefs {
		appendTreeNode(l, rootRef, result.RefToMeta, result.ChildRefs)
	}
	l.Render()
}

func appendTreeNode(l list.Writer, ref string, refToMeta map[string]core.FileMeta, children map[string][]string) {
	meta := refToMeta[ref]

	label := meta.Name
	if meta.Type == core.FileTypeFile {
		label += fmt.Sprintf(" (%s)", formatBytes(meta.Size))
	}
	l.AppendItem(label)

	kids := children[ref]
	if len(kids) == 0 {
		return
	}

	sort.Slice(kids, func(i, j int) bool {
		return refToMeta[kids[i]].Name < refToMeta[kids[j]].Name
	})

	l.Indent()
	for _, childRef := range kids {
		appendTreeNode(l, childRef, refToMeta, children)
	}
	l.UnIndent()
}

func runBreakLock() {
	blCmd := flag.NewFlagSet("break-lock", flag.ExitOnError)
	g := addGlobalFlags(blCmd)
	_ = blCmd.Parse(os.Args[2:])

	ctx := context.Background()

	client, err := g.openClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}

	removed, err := client.BreakLock(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to break lock: %v\n", err)
		os.Exit(1)
	}

	if len(removed) == 0 {
		fmt.Fprintln(os.Stderr, "No lock found — repository is not locked.")
		return
	}

	fmt.Fprintf(os.Stderr, "Locks removed:\n")
	for _, r := range removed {
		fmt.Fprintf(os.Stderr, "  Operation:  %s\n", r.Operation)
		fmt.Fprintf(os.Stderr, "  Holder:     %s\n", r.Holder)
		fmt.Fprintf(os.Stderr, "  Acquired:   %s\n", r.AcquiredAt)
		fmt.Fprintf(os.Stderr, "  Expired at: %s\n", r.ExpiresAt)
		fmt.Fprintf(os.Stderr, "  Shared:     %v\n\n", r.IsShared)
	}
}

func runPrune() {
	pruneCmd := flag.NewFlagSet("prune", flag.ExitOnError)
	g := addGlobalFlags(pruneCmd)
	dryRun := pruneCmd.Bool("dry-run", false, "Show what would be deleted without deleting")

	_ = pruneCmd.Parse(os.Args[2:])

	ctx := context.Background()

	client, err := g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	var pruneOpts []cloudstic.PruneOption
	if *dryRun {
		pruneOpts = append(pruneOpts, engine.WithPruneDryRun())
	}
	if *g.verbose {
		pruneOpts = append(pruneOpts, engine.WithPruneVerbose())
	}
	result, err := client.Prune(ctx, pruneOpts...)
	if err != nil {
		fmt.Printf("Prune failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	printPruneStats(result)
}

func printPruneStats(r *engine.PruneResult) {
	if r.DryRun {
		fmt.Printf("Prune dry run complete.\n")
		fmt.Printf("  Objects scanned:       %d\n", r.ObjectsScanned)
		fmt.Printf("  Objects would delete:  %d\n", r.ObjectsDeleted)
	} else {
		fmt.Printf("Prune complete.\n")
		fmt.Printf("  Objects scanned:  %d\n", r.ObjectsScanned)
		fmt.Printf("  Objects deleted:  %d\n", r.ObjectsDeleted)
		fmt.Printf("  Space reclaimed:  %s\n", formatBytes(r.BytesReclaimed))
	}
}

func formatBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.1f TiB", float64(b)/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func printBackupSummary(r *engine.RunResult) {
	total := r.FilesNew + r.FilesChanged + r.FilesUnmodified +
		r.DirsNew + r.DirsChanged + r.DirsUnmodified
	if r.DryRun {
		fmt.Printf("\nBackup dry run complete.\n")
	} else {
		fmt.Printf("\nBackup complete. Snapshot: %s, Root: %s\n", r.SnapshotRef, r.Root)
	}
	fmt.Printf("Files:  %d new,  %d changed,  %d unmodified,  %d removed\n",
		r.FilesNew, r.FilesChanged, r.FilesUnmodified, r.FilesRemoved)
	fmt.Printf("Dirs:   %d new,  %d changed,  %d unmodified,  %d removed\n",
		r.DirsNew, r.DirsChanged, r.DirsUnmodified, r.DirsRemoved)
	if !r.DryRun {
		fmt.Printf("Added to the repository: %s (%s compressed)\n",
			formatBytes(r.BytesAddedRaw), formatBytes(r.BytesAddedStored))
	}
	fmt.Printf("Processed %d entries in %s\n",
		total, r.Duration.Round(time.Second))
	if !r.DryRun {
		fmt.Printf("Snapshot %s saved\n", r.SnapshotHash)
	}
}

func runBackup() {
	bkCmd := flag.NewFlagSet("backup", flag.ExitOnError)
	sourceType := bkCmd.String("source", envDefault("CLOUDSTIC_SOURCE", "gdrive"), "source type (gdrive, gdrive-changes, local, onedrive, onedrive-changes)")
	sourcePath := bkCmd.String("source-path", envDefault("CLOUDSTIC_SOURCE_PATH", "."), "Local source path (if source=local)")
	driveID := bkCmd.String("drive-id", envDefault("CLOUDSTIC_DRIVE_ID", ""), "Shared drive ID for gdrive source (omit for My Drive)")
	rootFolder := bkCmd.String("root-folder", envDefault("CLOUDSTIC_ROOT_FOLDER", ""), "Root folder ID for gdrive source (defaults to entire drive)")
	g := addGlobalFlags(bkCmd)
	dryRun := bkCmd.Bool("dry-run", false, "Scan source and report changes without writing to the store")

	var tags stringArrayFlags
	bkCmd.Var(&tags, "tag", "Tag to apply to the snapshot (can be specified multiple times)")

	_ = bkCmd.Parse(os.Args[2:])

	ctx := context.Background()

	src, err := initSource(*sourceType, *sourcePath, *driveID, *rootFolder, g)
	if err != nil {
		fmt.Printf("Failed to init source: %v\n", err)
		os.Exit(1)
	}

	client, err := g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	var backupOpts []cloudstic.BackupOption
	if *g.verbose {
		backupOpts = append(backupOpts, cloudstic.WithVerbose())
	}
	if *dryRun {
		backupOpts = append(backupOpts, engine.WithBackupDryRun())
	}
	if len(tags) > 0 {
		backupOpts = append(backupOpts, cloudstic.WithTags(tags...))
	}
	result, err := client.Backup(ctx, src, backupOpts...)
	if err != nil {
		fmt.Printf("Backup failed: %v\n", err)
		os.Exit(1)
	}
	printBackupSummary(result)
}

func runRestore() {
	rsCmd := flag.NewFlagSet("restore", flag.ExitOnError)
	g := addGlobalFlags(rsCmd)
	output := rsCmd.String("output", "./restore.zip", "Output ZIP file path")
	dryRun := rsCmd.Bool("dry-run", false, "Show what would be restored without writing the archive")

	_ = rsCmd.Parse(reorderArgs(rsCmd, os.Args[2:]))

	snapshotRef := "latest"
	if rsCmd.NArg() > 0 {
		snapshotRef = rsCmd.Arg(0)
	}

	ctx := context.Background()

	client, err := g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	var restoreOpts []cloudstic.RestoreOption
	if *dryRun {
		restoreOpts = append(restoreOpts, engine.WithRestoreDryRun())
	}
	if *g.verbose {
		restoreOpts = append(restoreOpts, engine.WithRestoreVerbose())
	}

	if *dryRun {
		result, err := client.Restore(ctx, io.Discard, snapshotRef, restoreOpts...)
		if err != nil {
			fmt.Printf("Restore failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nRestore dry run complete. Snapshot: %s\n", result.SnapshotRef)
		fmt.Printf("  Files: %d, Dirs: %d\n", result.FilesWritten, result.DirsWritten)
		fmt.Printf("  Estimated size: %s\n", formatBytes(result.BytesWritten))
		return
	}

	f, err := os.Create(*output)
	if err != nil {
		fmt.Printf("Failed to create output file: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()

	result, err := client.Restore(ctx, f, snapshotRef, restoreOpts...)
	if err != nil {
		_ = os.Remove(*output)
		fmt.Printf("Restore failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nRestore complete. Snapshot: %s\n", result.SnapshotRef)
	fmt.Printf("  Files: %d, Dirs: %d", result.FilesWritten, result.DirsWritten)
	if result.Errors > 0 {
		fmt.Printf(", Errors: %d", result.Errors)
	}
	fmt.Println()
	fmt.Printf("  Archive: %s (%s)\n", *output, formatBytes(result.BytesWritten))
}

func runList() {
	lsCmd := flag.NewFlagSet("list", flag.ExitOnError)
	g := addGlobalFlags(lsCmd)

	_ = lsCmd.Parse(os.Args[2:])

	ctx := context.Background()

	client, err := g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}
	result, err := client.List(ctx)
	if err != nil {
		fmt.Printf("List failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%d snapshots\n", len(result.Snapshots))
	renderSnapshotTable(result.Snapshots, nil)
}

func runLsSnapshot() {
	lsCmd := flag.NewFlagSet("ls", flag.ExitOnError)
	g := addGlobalFlags(lsCmd)

	_ = lsCmd.Parse(reorderArgs(lsCmd, os.Args[2:]))

	snapshotID := "latest"
	if lsCmd.NArg() > 0 {
		snapshotID = lsCmd.Arg(0)
	}

	ctx := context.Background()

	client, err := g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}
	start := time.Now()
	result, err := client.LsSnapshot(ctx, snapshotID)
	if err != nil {
		fmt.Printf("Ls failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Listing files for snapshot: %s (Created: %s)\n", result.Ref, result.Snapshot.Created)
	renderSnapshotTree(result)
	fmt.Printf("\n%d entries listed in %s\n", len(result.RefToMeta), time.Since(start).Round(time.Millisecond))
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

// stringArrayFlags implements flag.Value interface for multiple string flags
type stringArrayFlags []string

func (i *stringArrayFlags) String() string {
	return fmt.Sprint(*i)
}

func (i *stringArrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}
