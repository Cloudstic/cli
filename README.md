# Cloudstic CLI

Content-addressable, encrypted backup tool for Google Drive, OneDrive, and local files.

## Features

- **Encrypted by default** — AES-256-GCM encryption with password, platform key, or recovery key slots
- **Content-addressable storage** — Deduplication across sources; identical files stored only once
- **Incremental backups** — Only changed files are stored
- **Multiple sources** — Google Drive, OneDrive, local directories
- **Multiple backends** — Local filesystem or Backblaze B2
- **Retention policies** — Keep-last, hourly, daily, weekly, monthly, yearly
- **Point-in-time restore** — Restore any snapshot, any file, any time

## Quick Start

```bash
go install github.com/cloudstic/cli/cmd/cloudstic@latest

# Initialize an encrypted repository
cloudstic init -encryption-password "my passphrase"

# Back up a local directory
cloudstic backup -source local -source-path ~/Documents -encryption-password "my passphrase"

# Back up Google Drive
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/credentials.json
cloudstic backup -source gdrive -encryption-password "my passphrase"

# List snapshots
cloudstic list -encryption-password "my passphrase"

# Restore
cloudstic restore -target ./restored -encryption-password "my passphrase"
```

## Documentation

See the [User Guide](docs/user-guide.md) for complete documentation covering all commands, sources, storage backends, encryption, and retention policies.

## Cloud Service

Don't want to manage infrastructure? [Cloudstic Cloud](https://cloudstic.com) handles scheduling, storage, and retention automatically. Same engine, zero ops.

## License

MIT
