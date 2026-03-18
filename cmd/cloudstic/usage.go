package main

import (
	"fmt"
	"os"

	"github.com/cloudstic/cli/internal/ui"
)

func printUsage() {
	t := ui.NewTermWriter(os.Stdout)

	_, _ = fmt.Fprintf(t.W, "%sCloudstic%s — Content-Addressable Backup System\n", ui.Bold, ui.Reset)

	t.Heading("USAGE")
	_, _ = fmt.Fprintf(t.W, "  cloudstic %s<command>%s [options]\n", ui.Cyan, ui.Reset)

	t.Heading("COMMANDS")
	t.Commands([][2]string{
		{"init", "Initialize a new repository (must run before first backup)"},
		{"backup", "Create a new backup snapshot from a source"},
		{"auth new", "Create or update a reusable cloud auth entry"},
		{"auth list", "List auth entries from profiles.yaml"},
		{"auth show", "Show one auth entry"},
		{"auth login", "Run OAuth login flow for one auth entry"},
		{"store new", "Create or update a store entry in profiles.yaml"},
		{"store list", "List configured stores"},
		{"store show", "Show one store and its configuration"},
		{"store verify", "Verify one store's credentials and connectivity"},
		{"profile new", "Create or update a backup profile in profiles.yaml"},
		{"profile list", "List stores, auth entries, and backup profiles"},
		{"profile show", "Show one profile and resolved store/auth references"},
		{"restore", "Restore files from a backup snapshot"},
		{"list", "List all backup snapshots in the repository"},
		{"ls", "List files within a specific snapshot"},
		{"prune", "Remove unused data chunks from the repository"},
		{"forget", "Remove a specific snapshot from history"},
		{"diff", "Compare two snapshots or a snapshot against latest"},
		{"break-lock", "Remove a stale repository lock left by a crashed process"},
		{"key list", "List all encryption key slots in the repository"},
		{"key add-recovery", "Generate a 24-word recovery key for an encrypted repository"},
		{"key passwd", "Change the repository password"},
		{"check", "Verify repository integrity (reference chain, objects, data)"},
		{"cat", "Display raw JSON content of repository objects"},
		{"completion", "Generate shell completion scripts (bash, zsh, fish)"},
	})

	t.HeadingSub("GLOBAL OPTIONS", "(also settable via env vars)")
	t.Flags([][2]string{
		{"-store <uri>", ui.Env("Storage backend URI (see formats below)", "CLOUDSTIC_STORE")},
		{"-profile <name>", ui.Env("Profile name from profiles.yaml", "CLOUDSTIC_PROFILE")},
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
		{"-s3-endpoint <url>", ui.Env("S3 compatible endpoint (for MinIO, R2, etc.)", "CLOUDSTIC_S3_ENDPOINT")},
		{"-s3-region <region>", ui.Env("S3 region", "CLOUDSTIC_S3_REGION")},
		{"-s3-profile <name>", ui.Env("AWS shared config profile for S3 auth", "CLOUDSTIC_S3_PROFILE / AWS_PROFILE")},
		{"-s3-access-key <key>", ui.Env("S3 Access Key ID", "AWS_ACCESS_KEY_ID")},
		{"-s3-secret-key <secret>", ui.Env("S3 Secret Access Key", "AWS_SECRET_ACCESS_KEY")},
		{"-source-sftp-password <pw>", ui.Env("SFTP source password", "CLOUDSTIC_SOURCE_SFTP_PASSWORD")},
		{"-source-sftp-key <path>", ui.Env("Path to SSH private key for SFTP source", "CLOUDSTIC_SOURCE_SFTP_KEY")},
		{"-store-sftp-password <pw>", ui.Env("SFTP store password", "CLOUDSTIC_STORE_SFTP_PASSWORD")},
		{"-store-sftp-key <path>", ui.Env("Path to SSH private key for SFTP store", "CLOUDSTIC_STORE_SFTP_KEY")},
		{"-no-prompt", "Disable interactive prompts (for scripts and CI)"},
		{"-verbose", "Log detailed file-level operations"},
		{"-quiet", "Suppress progress bars (keeps final summary)"},
		{"-debug", "Log every store request (network calls, timing, sizes)"},
	})
	t.Blank()
	t.Note(
		"Store URI formats:",
		"  local:<path>                       e.g. local:./backup_store",
		"  s3:<bucket>[/<prefix>]             e.g. s3:my-bucket or s3:my-bucket/prod",
		"  b2:<bucket>[/<prefix>]             e.g. b2:my-bucket or b2:my-bucket/prod",
		"  sftp://[user@]host[:port]/<path>   e.g. sftp://backup@host.com/backups",
	)

	t.Heading("ENCRYPTION OPTIONS")
	t.Flags([][2]string{
		{"-password <pw>", ui.Env("Repository password", "CLOUDSTIC_PASSWORD")},
		{"-prompt", "Prompt for password interactively (use alongside --encryption-key or --kms-key-arn)"},
		{"-encryption-key <hex>", ui.Env("Platform key (64 hex chars = 32 bytes)", "CLOUDSTIC_ENCRYPTION_KEY")},
		{"-recovery-key <words>", ui.Env("Recovery key (24-word seed phrase)", "CLOUDSTIC_RECOVERY_KEY")},
		{"-kms-key-arn <arn>", ui.Env("AWS KMS key ARN for kms-platform slots", "CLOUDSTIC_KMS_KEY_ARN")},
		{"-kms-region <region>", ui.Env("AWS KMS region", "CLOUDSTIC_KMS_REGION")},
		{"-kms-endpoint <url>", ui.Env("AWS KMS endpoint URL", "CLOUDSTIC_KMS_ENDPOINT")},
	})
	t.Blank()
	t.Note(
		"Encryption is required by default (AES-256-GCM). Provide -password",
		"or -encryption-key when running 'cloudstic init'. Use -recovery-key to open a",
		"repository with a recovery seed phrase.",
	)

	t.Heading("COMMAND DETAILS")

	t.Command("init", "")
	t.Flags([][2]string{
		{"-add-recovery-key", "Generate a 24-word recovery key during init"},
		{"-no-encryption", "Create an unencrypted repository (not recommended)"},
		{"-adopt-slots", "Initialize by adopting existing key slots if found"},
	})
	t.Blank()

	t.Command("key list", "")
	t.Note("  List all encryption key slots present in the repository.")
	t.Blank()

	t.Command("key add-recovery", "")
	t.Note(
		"  Generate a 24-word recovery key for an existing encrypted repository.",
		"  Requires -encryption-key or -encryption-password to unlock the master key.",
	)
	t.Blank()

	t.Command("key passwd", "")
	t.Flags([][2]string{
		{"-new-password <pw>", "New password (prompted interactively if not set)"},
	})
	t.Note(
		"  Change the repository password. Provide current credentials via",
		"  -password, -encryption-key, or -kms-key-arn to unlock.",
	)
	t.Blank()

	t.Command("backup", "")
	t.Flags([][2]string{
		{"-source <uri>", ui.Env("Source URI: local:<path>, sftp://[user@]host[:port]/<path>, gdrive[://<Drive Name>][/<path>], gdrive-changes[://<Drive Name>][/<path>], onedrive[://<Drive Name>][/<path>], onedrive-changes[://<Drive Name>][/<path>]", "CLOUDSTIC_SOURCE")},
		{"-profile <name>", "Run backup from a named profile"},
		{"-all-profiles", "Run backup for all enabled profiles"},
		{"-auth-ref <name>", "Use named auth entry from profiles.yaml for cloud credentials"},
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
		{"-skip-native-files", "Exclude Google-native files (Docs, Sheets, Slides, etc.)"},
		{"-google-credentials <path>", ui.Env("Path to Google service account credentials JSON", "GOOGLE_APPLICATION_CREDENTIALS")},
		{"-google-token-file <path>", ui.Env("Path to Google OAuth token file", "GOOGLE_TOKEN_FILE")},
		{"-onedrive-client-id <id>", ui.Env("OneDrive OAuth client ID", "ONEDRIVE_CLIENT_ID")},
		{"-onedrive-token-file <path>", ui.Env("Path to OneDrive OAuth token file", "ONEDRIVE_TOKEN_FILE")},
		{"-tag <tag>", "Tag to apply to the snapshot (repeatable)"},
		{"-exclude <pattern>", "Exclude pattern, gitignore syntax (repeatable)"},
		{"-exclude-file <path>", "Load exclude patterns from file (one per line, gitignore syntax)"},
		{"-dry-run", "Scan source and report changes without writing to the store"},
		{"-skip-mode", "Skip POSIX mode, uid, gid, btime, and flags collection"},
		{"-skip-flags", "Skip file flags collection"},
		{"-skip-xattrs", "Skip extended attribute collection"},
		{"-xattr-namespaces <prefixes>", "Restrict xattr collection to prefixes (comma-separated)"},
	})
	t.Blank()
	t.Note(
		"Source URI formats:",
		"  local:<path>                       e.g. local:./documents",
		"  sftp://[user@]host[:port]/<path>   e.g. sftp://backup@host.com/data",
		"  gdrive                             Google Drive (full scan)",
		"  gdrive-changes                     Google Drive (incremental via Changes API)",
		"  onedrive                           OneDrive (full scan)",
		"  onedrive-changes                   OneDrive (incremental via delta API)",
	)

	t.Command("store list", "")
	t.Flags([][2]string{{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")}})
	t.Note("  List configured stores.")
	t.Blank()

	t.Command("store show", "<name>")
	t.Flags([][2]string{{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")}})
	t.Note("  Show one store and its configuration.")
	t.Blank()

	t.Command("store verify", "<name>")
	t.Flags([][2]string{{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")}})
	t.Note("  Resolve store credentials and verify connectivity.")
	t.Blank()

	t.Command("store init", "<name>")
	t.Flags([][2]string{
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
		{"-yes", "Initialize without confirmation prompt"},
	})
	t.Note("  Initialize a configured store by reference from profiles.yaml.")
	t.Blank()

	t.Command("store new", "")
	t.Flags([][2]string{
		{"-name <name>", "Store reference name"},
		{"-uri <uri>", "Store URI (e.g. s3:bucket/path, local:/path, sftp://host/path)"},
		{"-s3-region <region>", "S3 region"},
		{"-s3-profile <name>", "AWS shared config profile"},
		{"-s3-endpoint <url>", "S3-compatible endpoint URL"},
		{"-s3-access-key <key>", "S3 static access key"},
		{"-s3-secret-key <key>", "S3 static secret key"},
		{"-s3-access-key-secret <ref>", "Secret reference for S3 access key (env://, keychain://)"},
		{"-s3-secret-key-secret <ref>", "Secret reference for S3 secret key (env://, keychain://)"},
		{"-store-sftp-password <pass>", "SFTP password"},
		{"-store-sftp-key <path>", "Path to SFTP private key"},
		{"-store-sftp-password-secret <ref>", "Secret reference for SFTP password (env://, keychain://)"},
		{"-store-sftp-key-secret <ref>", "Secret reference for SFTP key path (env://, keychain://)"},
		{"-password-secret <ref>", "Secret reference for repository password (env://, keychain://)"},
		{"-encryption-key-secret <ref>", "Secret reference for platform key (env://, keychain://)"},
		{"-recovery-key-secret <ref>", "Secret reference for recovery key mnemonic (env://, keychain://)"},
		{"-kms-key-arn <arn>", "AWS KMS key ARN for envelope encryption"},
		{"-kms-region <region>", "AWS KMS region"},
		{"-kms-endpoint <url>", "Custom AWS KMS endpoint URL"},
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
	})
	t.Note("  Create or update a store entry in profiles.yaml.",
		"  Prefer secret refs: -password-secret / -encryption-key-secret / -recovery-key-secret.",
		"  KMS settings are stored directly (ARN is not a secret).",
	)
	t.Blank()

	t.Command("profile list", "")
	t.Flags([][2]string{
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
	})
	t.Note("  List configured stores, auth entries, and backup profiles.")
	t.Blank()

	t.Command("profile show", "<name>")
	t.Flags([][2]string{
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
	})
	t.Note("  Show one profile and resolved store/auth references.")
	t.Blank()

	t.Command("profile new", "")
	t.Flags([][2]string{
		{"-name <name>", "Profile name"},
		{"-source <uri>", "Source URI for this profile"},
		{"-store-ref <name>", "Store reference name to use from top-level stores"},
		{"-store <uri>", "Optional store URI to create/update under -store-ref"},
		{"-auth-ref <name>", "Auth reference name to use from top-level auth"},
		{"-tag <tag>", "Tag for snapshots (repeatable)"},
		{"-exclude <pattern>", "Exclude pattern (repeatable)"},
		{"-exclude-file <path>", "Path to exclude file"},
		{"-skip-native-files", "Exclude Google-native files"},
		{"-volume-uuid <uuid>", "Override local source volume UUID"},
		{"-google-credentials <path>", "Path to Google service account credentials JSON"},
		{"-google-token-file <path>", "Path to Google OAuth token file"},
		{"-onedrive-client-id <id>", "OneDrive OAuth client ID"},
		{"-onedrive-token-file <path>", "Path to OneDrive OAuth token file"},
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
	})
	t.Note(
		"  Create or update a backup profile in profiles.yaml.",
		"  Use -store-ref by itself to reference an existing store entry.",
		"  Add -store to create or update that store entry in the same command.",
		"  Use -auth-ref to reuse cloud OAuth settings across multiple profiles.",
	)
	t.Blank()

	t.Command("auth list", "")
	t.Flags([][2]string{{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")}})
	t.Note("  List configured auth entries.")
	t.Blank()

	t.Command("auth show", "<name>")
	t.Flags([][2]string{{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")}})
	t.Note("  Show one auth entry.")
	t.Blank()

	t.Command("auth new", "")
	t.Flags([][2]string{
		{"-name <name>", "Auth reference name"},
		{"-provider <google|onedrive>", "Cloud provider for this auth entry"},
		{"-google-credentials <path>", "Google service account credentials JSON (optional)"},
		{"-google-token-file <path>", "Google OAuth token file path (required for provider=google)"},
		{"-onedrive-client-id <id>", "OneDrive OAuth client ID (optional)"},
		{"-onedrive-token-file <path>", "OneDrive OAuth token file path (required for provider=onedrive)"},
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
	})
	t.Note("  Create or update a reusable cloud auth entry.")
	t.Blank()

	t.Command("auth login", "")
	t.Flags([][2]string{
		{"-name <name>", "Auth reference name"},
		{"-profiles-file <path>", ui.Env("Path to profiles YAML file", "CLOUDSTIC_PROFILES_FILE")},
	})
	t.Note("  Trigger OAuth login for an auth entry and save token to its configured file.")
	t.Blank()

	t.Command("restore", "[snapshot_id]")
	t.Flags([][2]string{
		{"-output <path>", "Output path (ZIP file for -format zip, directory for -format dir)"},
		{"-format <zip|dir>", "Restore format (default: auto from -output)"},
		{"-path <path>", "Restore only the given file or subtree (e.g. Documents/report.pdf or Documents/)"},
		{"-dry-run", "Show what would be restored without writing output"},
	})
	t.Blank()

	t.Command("list", "")
	t.Flags([][2]string{
		{"-group", "Group snapshots by source identity"},
	})
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
		{"-source <uri>", "Filter by source URI (e.g. local:./docs, gdrive, sftp://host/path)"},
		{"-account <id>", "Filter by account"},
		{"-tag <tag>", "Filter by tag (repeatable)"},
		{"-group-by <fields>", "Group snapshots by fields (default: source,account,path)"},
	})
	t.Blank()

	t.Command("diff", "<snapshot_1> <snapshot_2>")
	t.Note("  Compare two snapshots. Use 'latest' as an alias for the most recent.")
	t.Blank()

	t.Command("break-lock", "")
	t.Note("  Remove a stale repository lock left by a crashed or killed process.",
		"  Only use this if you are sure no other operation is running.")
	t.Blank()

	t.Command("check", "[snapshot_id]")
	t.Flags([][2]string{
		{"-read-data", "Re-hash all chunk data for full byte-level verification"},
	})
	t.Note("  Verify the integrity of the repository by walking the full reference",
		"  chain: index/latest → snapshot → HAMT nodes → filemeta → content → chunks.",
		"  Defaults to the latest snapshot. Reports missing, corrupt, or unreadable objects.")
	t.Blank()

	t.Command("cat", "<object_key> [object_key...]")
	t.Flags([][2]string{
		{"-json", "Suppress non-JSON output (alias for -quiet)"},
	})
	t.Note("  Display raw JSON for one or more repository objects.",
		"  Object keys: snapshot/<hash>, filemeta/<hash>, content/<hash>,",
		"  node/<hash>, chunk/<hash>, config, index/latest, keys/<slot>")

	t.Command("completion", "<shell>")
	t.Note("  Generate completion scripts for bash, zsh, or fish.",
		"  See 'cloudstic completion --help' or the user guide for setup instructions.")
	t.Blank()

	t.Heading("EXAMPLES")
	t.Examples(
		`cloudstic init -password "my secret passphrase"`,
		`cloudstic init -password "my secret passphrase" -add-recovery-key`,
		"cloudstic backup -source local:./documents",
		"cloudstic backup -source gdrive -store b2:my-bucket",
		"cloudstic backup -source sftp://backup@host.com/data -source-sftp-key ~/.ssh/id_ed25519",
		"cloudstic list",
		"cloudstic restore",
		"cloudstic restore abc123 -output ./my-backup.zip",
		"cloudstic restore abc123 -format dir -output ./restored",
		"cloudstic restore abc123 -path Documents/report.pdf",
		"cloudstic restore abc123 -path Documents/",
		"cloudstic backup -source local:./documents -dry-run",
		"cloudstic prune -dry-run -verbose",
	)
	t.Blank()
}
