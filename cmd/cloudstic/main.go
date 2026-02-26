package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/engine"
	"github.com/cloudstic/cli/pkg/paths"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/ui"

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
	fmt.Println(`Cloudstic - Content-Addressable Backup System

Usage:
  cloudstic <command> [options]

Commands:
  init              Initialize a new repository (must run before first backup)
  backup            Create a new backup snapshot from a source
  restore           Restore files from a backup snapshot
  list              List all backup snapshots in the repository
  ls                List files within a specific snapshot
  prune             Remove unused data chunks from the repository
  forget            Remove a specific snapshot from history
  diff              Compare two snapshots or a snapshot against latest
  add-recovery-key  Generate a recovery key for an existing encrypted repository

Global Options (also settable via env vars):
  -store <type>        Type of backup storage (local, b2, hybrid) [env: CLOUDSTIC_STORE, default: local]
  -store-path <path>   Path to local storage or B2 bucket name   [env: CLOUDSTIC_STORE_PATH, default: ./backup_store]
  -store-prefix <pfx>  Key prefix for B2 objects                 [env: CLOUDSTIC_STORE_PREFIX]
  -database-url <url>  PostgreSQL URL (required for hybrid)      [env: CLOUDSTIC_DATABASE_URL]
  -tenant-id <uuid>    Tenant ID for hybrid store RLS            [env: CLOUDSTIC_TENANT_ID]

Encryption Options:
  -encryption-key <hex>      Platform key (64 hex chars = 32 bytes)   [env: CLOUDSTIC_ENCRYPTION_KEY]
  -encryption-password <pw>  Password for password-based encryption   [env: CLOUDSTIC_ENCRYPTION_PASSWORD]
  -recovery-key <mnemonic>   Recovery key (24-word seed phrase)       [env: CLOUDSTIC_RECOVERY_KEY]

  Encryption is REQUIRED by default (AES-256-GCM). Provide --encryption-password
  or --encryption-key when running 'cloudstic init'. Provide both to create
  dual-access (platform + password) key slots.
  Use --recovery-key to open a repository with a recovery seed phrase.

Command Specifics:

  init
    -recovery             Generate a 24-word recovery key during init
    -no-encryption        Create an unencrypted repository (not recommended)
    Initializes a new repository. Encryption credentials are required unless
    --no-encryption is explicitly passed. Use --recovery to also create a
    recovery key slot.
    For web-created hybrid repos, the web handles initialization during signup.

  add-recovery-key
    Generates a 24-word recovery key for an existing encrypted repository.
    Requires --encryption-key or --encryption-password to unlock the master key.

  backup
    -source <type>        source type (local, gdrive, gdrive-changes, onedrive) [default: gdrive]
    -source-path <path>   Path to local source directory (if source=local)
    -drive-id <id>        Shared drive ID for gdrive (omit for My Drive)
    -root-folder <id>     Root folder ID for gdrive (defaults to entire drive)
    -tag <tag>            Tag to apply to the snapshot (can be specified multiple times)
    -verbose              Enable verbose output

    Environment Variables:
      gdrive:   GOOGLE_APPLICATION_CREDENTIALS, GOOGLE_TOKEN_FILE
      onedrive: ONEDRIVE_CLIENT_ID, ONEDRIVE_CLIENT_SECRET, ONEDRIVE_TOKEN_FILE
      b2:       B2_KEY_ID, B2_APP_KEY

    Token files are stored in the cloudstic config directory by default:
      Linux:   ~/.config/cloudstic/
      macOS:   ~/Library/Application Support/cloudstic/
      Windows: %AppData%\cloudstic\
    Override with CLOUDSTIC_CONFIG_DIR or the per-source token env vars above.

  restore
    -target <path>        Directory to restore files to [default: ./restore_out]
    -snapshot <hash>      Snapshot ID to restore (defaults to latest)

  list
    (No additional flags)

  ls <snapshot_id>
    Lists files in the specified snapshot (defaults to latest if omitted)

  prune
    (No additional flags)

  forget <snapshot_id>
    -prune                Run prune immediately after forgetting [default: false]

  diff <snapshot_id_1> <snapshot_id_2>
    Compares two snapshots. Use 'latest' as an alias for the most recent snapshot.

Examples:
  cloudstic init -encryption-password "my secret passphrase"
  cloudstic init -encryption-password "my secret passphrase" -recovery -store b2 -store-path my-bucket
  cloudstic backup -source local -source-path ./documents -encryption-password "my secret passphrase"
  cloudstic backup -source gdrive -encryption-password "my secret passphrase" -store b2 -store-path my-bucket
  cloudstic list -encryption-password "my secret passphrase"
  cloudstic restore -snapshot latest -target ./restored -encryption-password "my secret passphrase"`)
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type globalFlags struct {
	storeType, storePath, storePrefix   *string
	databaseURL, tenantID               *string
	encryptionKey, encryptionPassword   *string
	recoveryKey                         *string
}

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	g.storeType = fs.String("store", envDefault("CLOUDSTIC_STORE", "local"), "store type (local, b2, hybrid)")
	g.storePath = fs.String("store-path", envDefault("CLOUDSTIC_STORE_PATH", "./backup_store"), "Local store path or B2 bucket name")
	g.storePrefix = fs.String("store-prefix", envDefault("CLOUDSTIC_STORE_PREFIX", ""), "Key prefix for B2 objects")
	g.databaseURL = fs.String("database-url", envDefault("CLOUDSTIC_DATABASE_URL", ""), "PostgreSQL URL (required for hybrid store)")
	g.tenantID = fs.String("tenant-id", envDefault("CLOUDSTIC_TENANT_ID", ""), "Tenant ID for hybrid store RLS")
	g.encryptionKey = fs.String("encryption-key", envDefault("CLOUDSTIC_ENCRYPTION_KEY", ""), "Platform key (hex-encoded, 32 bytes)")
	g.encryptionPassword = fs.String("encryption-password", envDefault("CLOUDSTIC_ENCRYPTION_PASSWORD", ""), "Password for password-based encryption")
	g.recoveryKey = fs.String("recovery-key", envDefault("CLOUDSTIC_RECOVERY_KEY", ""), "Recovery key (BIP39 24-word mnemonic)")
	return g
}

const configKey = "config"

// openStore creates the object store for commands that operate on an
// existing repository. It checks that the repo has been initialized (the
// "config" marker exists) and opens encryption if needed.
func (g *globalFlags) openStore() (store.ObjectStore, error) {
	raw, err := g.initObjectStore()
	if err != nil {
		return nil, err
	}

	cfg, err := loadRepoConfig(raw)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("repository not initialized -- run 'cloudstic init' first")
	}

	if !cfg.Encrypted {
		return raw, nil
	}

	platformKey, err := g.parsePlatformKey()
	if err != nil {
		return nil, err
	}
	if len(platformKey) == 0 && *g.encryptionPassword == "" && *g.recoveryKey == "" {
		return nil, fmt.Errorf("repository is encrypted -- provide --encryption-key, --encryption-password, or --recovery-key")
	}

	slots, err := g.loadKeySlots(raw)
	if err != nil {
		return nil, fmt.Errorf("load encryption key slots: %w", err)
	}
	if len(slots) == 0 {
		return nil, fmt.Errorf("repository is encrypted but no key slots found")
	}

	encKey, err := openExistingSlots(slots, platformKey, *g.encryptionPassword, *g.recoveryKey)
	if err != nil {
		return nil, err
	}
	return store.NewEncryptedStore(raw, encKey), nil
}

func loadRepoConfig(s store.ObjectStore) (*core.RepoConfig, error) {
	data, err := s.Get(configKey)
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
	return s.Put(configKey, data)
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
	initCmd.Parse(os.Args[2:])

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
	fmt.Fprintf(os.Stderr,  "║  %s\n", mnemonic)
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
	addCmd.Parse(os.Args[2:])

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
	case "hybrid":
		return g.initHybridStore()
	default:
		return nil, fmt.Errorf("unsupported store type: %s", *g.storeType)
	}
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

func initSource(sourceType, sourcePath, driveID, rootFolder string) (store.Source, error) {
	switch sourceType {
	case "local":
		return store.NewLocalSource(sourcePath), nil
	case "gdrive":
		creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		if creds == "" {
			return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS env var required for gdrive source")
		}
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
		creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		if creds == "" {
			return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS env var required for gdrive-changes source")
		}
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
		clientID := os.Getenv("ONEDRIVE_CLIENT_ID")
		clientSecret := os.Getenv("ONEDRIVE_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			return nil, fmt.Errorf("ONEDRIVE_CLIENT_ID and ONEDRIVE_CLIENT_SECRET env vars required")
		}
		tokenPath, err := resolveTokenPath("ONEDRIVE_TOKEN_FILE", "onedrive_token.json")
		if err != nil {
			return nil, err
		}
		return store.NewOneDriveSource(clientID, clientSecret, tokenPath)
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

	diffCmd.Parse(reorderArgs(diffCmd, os.Args[2:]))

	if diffCmd.NArg() < 2 {
		fmt.Println("Usage: cloudstic diff [options] <snapshot_id1> <snapshot_id2>")
		fmt.Println("       cloudstic diff [options] <snapshot_id1> latest")
		os.Exit(1)
	}
	snap1 := diffCmd.Arg(0)
	snap2 := diffCmd.Arg(1)

	ctx := context.Background()

	s, err := g.openStore()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	client := cloudstic.NewClient(s, cloudstic.WithReporter(ui.NewConsoleReporter()))
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

	forgetCmd.Parse(reorderArgs(forgetCmd, os.Args[2:]))

	hasPolicy := *keepLast > 0 || *keepHourly > 0 || *keepDaily > 0 ||
		*keepWeekly > 0 || *keepMonthly > 0 || *keepYearly > 0
	snapshotID := forgetCmd.Arg(0)

	if snapshotID == "" && !hasPolicy {
		fmt.Println("Usage: cloudstic forget [options] <snapshot_id>")
		fmt.Println("       cloudstic forget --keep-last n [--keep-daily n] [--prune] [--dry-run]")
		os.Exit(1)
	}

	ctx := context.Background()

	s, err := g.openStore()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	client := cloudstic.NewClient(s, cloudstic.WithReporter(ui.NewConsoleReporter()))

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

func runPrune() {
	pruneCmd := flag.NewFlagSet("prune", flag.ExitOnError)
	g := addGlobalFlags(pruneCmd)

	pruneCmd.Parse(os.Args[2:])

	ctx := context.Background()

	s, err := g.openStore()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	client := cloudstic.NewClient(s, cloudstic.WithReporter(ui.NewConsoleReporter()))
	result, err := client.Prune(ctx)
	if err != nil {
		fmt.Printf("Prune failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	printPruneStats(result)
}

func printPruneStats(r *engine.PruneResult) {
	fmt.Printf("Prune complete.\n")
	fmt.Printf("  Objects scanned:  %d\n", r.ObjectsScanned)
	fmt.Printf("  Objects deleted:  %d\n", r.ObjectsDeleted)
	fmt.Printf("  Space reclaimed:  %s\n", formatBytes(r.BytesReclaimed))
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
	fmt.Printf("\nBackup complete. Snapshot: %s, Root: %s\n", r.SnapshotRef, r.Root)
	fmt.Printf("Files:  %d new,  %d changed,  %d unmodified,  %d removed\n",
		r.FilesNew, r.FilesChanged, r.FilesUnmodified, r.FilesRemoved)
	fmt.Printf("Dirs:   %d new,  %d changed,  %d unmodified,  %d removed\n",
		r.DirsNew, r.DirsChanged, r.DirsUnmodified, r.DirsRemoved)
	fmt.Printf("Added to the repository: %s (%s compressed)\n",
		formatBytes(r.BytesAddedRaw), formatBytes(r.BytesAdded))
	fmt.Printf("Processed %d entries in %s\n",
		total, r.Duration.Round(time.Second))
	fmt.Printf("Snapshot %s saved\n", r.SnapshotHash)
}

func runBackup() {
	bkCmd := flag.NewFlagSet("backup", flag.ExitOnError)
	sourceType := bkCmd.String("source", "gdrive", "source type (gdrive, local, onedrive)")
	sourcePath := bkCmd.String("source-path", ".", "Local source path (if source=local)")
	driveID := bkCmd.String("drive-id", "", "Shared drive ID for gdrive source (omit for My Drive)")
	rootFolder := bkCmd.String("root-folder", "", "Root folder ID for gdrive source (defaults to entire drive)")
	g := addGlobalFlags(bkCmd)
	verbose := bkCmd.Bool("verbose", false, "Display verbose output")

	var tags stringArrayFlags
	bkCmd.Var(&tags, "tag", "Tag to apply to the snapshot (can be specified multiple times)")

	bkCmd.Parse(os.Args[2:])

	ctx := context.Background()

	src, err := initSource(*sourceType, *sourcePath, *driveID, *rootFolder)
	if err != nil {
		fmt.Printf("Failed to init source: %v\n", err)
		os.Exit(1)
	}

	dest, err := g.openStore()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	var backupOpts []cloudstic.BackupOption
	if *verbose {
		backupOpts = append(backupOpts, cloudstic.WithVerbose())
	}
	if len(tags) > 0 {
		backupOpts = append(backupOpts, cloudstic.WithTags(tags...))
	}
	client := cloudstic.NewClient(dest, cloudstic.WithReporter(ui.NewConsoleReporter()))
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
	targetPath := rsCmd.String("target", "./restore_out", "Target directory for restore")
	snapshot := rsCmd.String("snapshot", "", "Snapshot hash (optional, defaults to latest)")

	rsCmd.Parse(os.Args[2:])

	ctx := context.Background()

	s, err := g.openStore()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	client := cloudstic.NewClient(s, cloudstic.WithReporter(ui.NewConsoleReporter()))
	result, err := client.Restore(ctx, *targetPath, *snapshot)
	if err != nil {
		fmt.Printf("Restore failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nRestore complete. Snapshot: %s, Target: %s\n", result.SnapshotRef, result.TargetDir)
	fmt.Printf("Restored %d entries", result.Restored)
	if result.Errors > 0 {
		fmt.Printf(", %d errors", result.Errors)
	}
	fmt.Println()
}

func runList() {
	lsCmd := flag.NewFlagSet("list", flag.ExitOnError)
	g := addGlobalFlags(lsCmd)

	lsCmd.Parse(os.Args[2:])

	ctx := context.Background()

	s, err := g.openStore()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	client := cloudstic.NewClient(s, cloudstic.WithReporter(ui.NewConsoleReporter()))
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

	lsCmd.Parse(reorderArgs(lsCmd, os.Args[2:]))

	snapshotID := "latest"
	if lsCmd.NArg() > 0 {
		snapshotID = lsCmd.Arg(0)
	}

	ctx := context.Background()

	s, err := g.openStore()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	client := cloudstic.NewClient(s, cloudstic.WithReporter(ui.NewConsoleReporter()))
	result, err := client.LsSnapshot(ctx, snapshotID)
	if err != nil {
		fmt.Printf("Ls failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Listing files for snapshot: %s (Created: %s)\n", result.Ref, result.Snapshot.Created)
	renderSnapshotTree(result)
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
