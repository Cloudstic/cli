package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/source"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/moby/term"
)

// openStore initializes the raw object store with debug wrapping applied.
// Used by commands that operate on the store directly (init, key).
func (g *globalFlags) openStore() (store.ObjectStore, error) {
	if err := g.applyProfileStoreOverrides(); err != nil {
		return nil, err
	}
	raw, err := g.initObjectStore()
	if err != nil {
		return nil, err
	}
	return g.applyDebug(raw), nil
}

// applyDebug wraps a store with a DebugStore and enables the global debug
// logger when --debug is set. It returns the (possibly wrapped) store.
func (g *globalFlags) applyDebug(s store.ObjectStore) store.ObjectStore {
	if g.debug == nil || !*g.debug {
		return s
	}
	if g.debugLog == nil {
		g.debugLog = &ui.SafeLogWriter{}
	}
	logger.Writer = g.debugLog
	return store.NewDebugStore(s, g.debugLog)
}

func (g *globalFlags) openClient(ctx context.Context) (*cloudstic.Client, error) {
	if err := g.applyProfileStoreOverrides(); err != nil {
		return nil, err
	}
	raw, err := g.initObjectStore()
	if err != nil {
		return nil, err
	}
	raw = g.applyDebug(raw)

	packfileEnabled := g.disablePackfile == nil || !*g.disablePackfile

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

	kc, err := g.buildKeychain(ctx)
	if err != nil {
		return nil, err
	}

	return cloudstic.NewClient(ctx, raw,
		cloudstic.WithKeychain(kc),
		cloudstic.WithReporter(reporter),
		cloudstic.WithPackfile(packfileEnabled),
	)
}

func (g *globalFlags) applyProfileStoreOverrides() error {
	if g.profile == nil || *g.profile == "" {
		return nil
	}
	profilesFile := defaultProfilesFilename
	if g.profilesFile != nil && *g.profilesFile != "" {
		profilesFile = *g.profilesFile
	}
	cfg, err := cloudstic.LoadProfilesFile(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles file %q: %w", profilesFile, err)
	}
	p, ok := cfg.Profiles[*g.profile]
	if !ok {
		return fmt.Errorf("unknown profile %q", *g.profile)
	}
	if p.Store == "" {
		return nil
	}
	s, ok := cfg.Stores[p.Store]
	if !ok {
		return fmt.Errorf("profile %q references unknown store %q", *g.profile, p.Store)
	}
	flagsSet := map[string]bool{}
	for _, name := range []string{
		"store", "s3-endpoint", "s3-region", "s3-profile", "s3-access-key", "s3-secret-key",
		"store-sftp-password", "store-sftp-key",
		"password", "encryption-key", "recovery-key", "kms-key-arn", "kms-region", "kms-endpoint",
	} {
		flagsSet[name] = cliFlagProvided(name)
	}
	if err := applyProfileStoreToGlobalFlags(g, s, flagsSet); err != nil {
		return fmt.Errorf("profile %q store %q: %w", *g.profile, p.Store, err)
	}
	return nil
}

// buildKMSClient creates an AWS KMS client if -kms-key-arn is set, otherwise
// returns nil. The returned client implements both KMSEncrypter and KMSDecrypter.
func (g *globalFlags) buildKMSClient(ctx context.Context) (crypto.KMSClient, error) {
	if g.kmsKeyARN == nil || *g.kmsKeyARN == "" {
		return nil, nil
	}
	var opts []crypto.KMSClientOption
	if g.kmsRegion != nil && *g.kmsRegion != "" {
		opts = append(opts, crypto.WithKMSRegion(*g.kmsRegion))
	}
	if g.kmsEndpoint != nil && *g.kmsEndpoint != "" {
		opts = append(opts, crypto.WithKMSEndpoint(*g.kmsEndpoint))
	}
	client, err := crypto.NewAWSKMSClient(ctx, *g.kmsKeyARN, opts...)
	if err != nil {
		return nil, fmt.Errorf("init KMS client: %w", err)
	}
	return client, nil
}

// buildKeychain constructs a Keychain from the CLI flags.
func (g *globalFlags) buildKeychain(ctx context.Context) (keychain.Chain, error) {
	platformKey, err := g.parsePlatformKey()
	if err != nil {
		return nil, err
	}

	kmsClient, err := g.buildKMSClient(ctx)
	if err != nil {
		return nil, err
	}

	var chain keychain.Chain

	if kmsClient != nil {
		chain = append(chain, keychain.WithKMSClient(kmsClient))
	}
	if len(platformKey) > 0 {
		chain = append(chain, keychain.WithPlatformKey(platformKey))
	}
	if *g.password != "" {
		chain = append(chain, keychain.WithPassword(*g.password))
	}
	if *g.recoveryKey != "" {
		chain = append(chain, keychain.WithRecoveryKey(*g.recoveryKey))
	}
	promptRequested := g.prompt != nil && *g.prompt
	if (len(chain) == 0 || promptRequested) && !hasGlobalFlag("no-prompt") && term.IsTerminal(os.Stdin.Fd()) {
		chain = append(chain, keychain.WithPrompt(
			func() (string, error) { return ui.PromptPassword("Repository password") },
			func() (string, error) { return ui.PromptPasswordConfirm("Enter new repository password") },
		))
	}

	return chain, nil
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

// storeURIParts holds the parsed components of a --store URI.
type storeURIParts struct {
	scheme string // "local", "s3", "b2", "sftp"
	// S3/B2 fields
	bucket string
	prefix string
	// local field
	path string
	// SFTP fields
	host string
	port string
	user string
}

// parseStoreURI parses a --store flag value into its components.
//
// Supported formats:
//
//	local:<path>                        e.g. local:./backup_store
//	s3:<bucket>[/<prefix>]              e.g. s3:my-bucket or s3:my-bucket/prod
//	b2:<bucket>[/<prefix>]              e.g. b2:my-bucket or b2:my-bucket/prod
//	sftp://[user@]host[:port]/<path>    e.g. sftp://backup@host.com/backups
func parseStoreURI(raw string) (*storeURIParts, error) {
	if strings.HasPrefix(raw, "sftp://") {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid store URI %q: %w", raw, err)
		}
		if u.Hostname() == "" {
			return nil, fmt.Errorf("invalid store URI %q: sftp URI must include a hostname", raw)
		}
		user := ""
		if u.User != nil {
			user = u.User.Username()
		}
		return &storeURIParts{
			scheme: "sftp",
			host:   u.Hostname(),
			port:   u.Port(),
			user:   user,
			path:   u.Path,
		}, nil
	}

	idx := strings.IndexByte(raw, ':')
	if idx < 0 {
		return nil, fmt.Errorf("invalid store URI %q: missing scheme (e.g. local:./path, s3:bucket, b2:bucket, sftp://host/path)", raw)
	}
	scheme := raw[:idx]
	rest := raw[idx+1:]

	switch scheme {
	case "local":
		if rest == "" {
			return nil, fmt.Errorf("invalid store URI %q: local path cannot be empty", raw)
		}
		return &storeURIParts{scheme: "local", path: rest}, nil
	case "s3", "b2":
		if rest == "" {
			return nil, fmt.Errorf("invalid store URI %q: bucket name cannot be empty", raw)
		}
		bucket, prefix, _ := strings.Cut(rest, "/")
		if bucket == "" {
			return nil, fmt.Errorf("invalid store URI %q: bucket name cannot be empty", raw)
		}
		return &storeURIParts{scheme: scheme, bucket: bucket, prefix: prefix}, nil
	default:
		return nil, fmt.Errorf("unknown store scheme %q in %q: supported schemes are local, s3, b2, sftp", scheme, raw)
	}
}

func (g *globalFlags) initObjectStore() (store.ObjectStore, error) {
	uri, err := parseStoreURI(*g.store)
	if err != nil {
		return nil, err
	}

	var inner store.ObjectStore
	switch uri.scheme {
	case "local":
		inner, err = store.NewLocalStore(uri.path)
	case "b2":
		keyID := os.Getenv("B2_KEY_ID")
		appKey := os.Getenv("B2_APP_KEY")
		if keyID == "" || appKey == "" {
			return nil, fmt.Errorf("B2_KEY_ID and B2_APP_KEY env vars required for b2 store")
		}
		inner, err = store.NewB2Store(uri.bucket, store.WithCredentials(keyID, appKey), store.WithPrefix(uri.prefix))
	case "s3":
		inner, err = store.NewS3Store(
			context.Background(),
			uri.bucket,
			store.WithS3Endpoint(*g.s3Endpoint),
			store.WithS3Region(*g.s3Region),
			store.WithS3Profile(*g.s3Profile),
			store.WithS3Credentials(*g.s3AccessKey, *g.s3SecretKey),
			store.WithS3Prefix(uri.prefix),
		)
	case "sftp":
		inner, err = store.NewSFTPStore(uri.host, g.buildSFTPStoreOpts(uri)...)
	default:
		return nil, fmt.Errorf("unsupported store type: %s", uri.scheme)
	}

	if err != nil {
		return nil, err
	}

	return inner, nil
}

func (g *globalFlags) buildSFTPStoreOpts(uri *storeURIParts) []store.SFTPStoreOption {
	opts := []store.SFTPStoreOption{
		store.WithSFTPBasePath(uri.path),
	}
	if uri.port != "" {
		opts = append(opts, store.WithSFTPPort(uri.port))
	}
	if uri.user != "" {
		opts = append(opts, store.WithSFTPUser(uri.user))
	}
	if pw := *g.storeSFTPPassword; pw != "" {
		opts = append(opts, store.WithSFTPPassword(pw))
	}
	if k := *g.storeSFTPKey; k != "" {
		opts = append(opts, store.WithSFTPKey(k))
	}
	return opts
}

// sourceURIParts holds the parsed components of a --source URI or keyword.
type sourceURIParts struct {
	scheme string // "local", "sftp", "gdrive", "gdrive-changes", "onedrive", "onedrive-changes"
	// local/sftp fields
	path string
	// sftp-specific fields
	host string
	port string
	user string
}

// parseSourceURI parses a --source flag value into its components.
//
// Supported formats:
//
//	local:<path>                        e.g. local:./documents
//	sftp://[user@]host[:port]/<path>    e.g. sftp://backup@host.com/data
//	gdrive
//	gdrive-changes
//	onedrive
//	onedrive-changes
func parseSourceURI(raw string) (*sourceURIParts, error) {
	if strings.HasPrefix(raw, "sftp://") {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid source URI %q: %w", raw, err)
		}
		if u.Hostname() == "" {
			return nil, fmt.Errorf("invalid source URI %q: sftp URI must include a hostname", raw)
		}
		user := ""
		if u.User != nil {
			user = u.User.Username()
		}
		return &sourceURIParts{
			scheme: "sftp",
			host:   u.Hostname(),
			port:   u.Port(),
			user:   user,
			path:   u.Path,
		}, nil
	}

	idx := strings.IndexByte(raw, ':')
	if idx >= 0 {
		scheme := raw[:idx]
		rest := raw[idx+1:]
		switch scheme {
		case "local":
			if rest == "" {
				return nil, fmt.Errorf("invalid source URI %q: local path cannot be empty", raw)
			}
			return &sourceURIParts{scheme: "local", path: rest}, nil
		case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
			if strings.HasPrefix(rest, "//") {
				// Format: scheme://Drive Name/path
				rest = rest[2:]
				idx := strings.IndexByte(rest, '/')
				driveName := ""
				path := "/"
				if idx >= 0 {
					driveName = rest[:idx]
					path = ensureLeadingSlash(rest[idx:])
				} else {
					driveName = rest
				}
				return &sourceURIParts{scheme: scheme, host: driveName, path: path}, nil
			}
			return &sourceURIParts{scheme: scheme, path: ensureLeadingSlash(rest)}, nil
		default:
			return nil, fmt.Errorf("unknown source scheme %q in %q: supported URI formats are local:<path> and sftp://[user@]host[:port]/<path>", scheme, raw)
		}
	}

	// Bare keyword (cloud sources)
	switch raw {
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		return &sourceURIParts{scheme: raw, path: "/"}, nil
	default:
		return nil, fmt.Errorf("unknown source %q: supported values are local:<path>, sftp://[user@]host[:port]/<path>, gdrive[:<path>], gdrive-changes[:<path>], onedrive[:<path>], onedrive-changes[:<path>]", raw)
	}
}

func ensureLeadingSlash(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") {
		return "/" + s
	}
	return s
}

func (g *globalFlags) buildSFTPSourceOpts(uri *sourceURIParts) []source.SFTPOption {
	opts := []source.SFTPOption{
		source.WithSFTPSourceBasePath(uri.path),
	}
	if uri.port != "" {
		opts = append(opts, source.WithSFTPSourcePort(uri.port))
	}
	if uri.user != "" {
		opts = append(opts, source.WithSFTPSourceUser(uri.user))
	}
	if pw := *g.sourceSFTPPassword; pw != "" {
		opts = append(opts, source.WithSFTPSourcePassword(pw))
	}
	if k := *g.sourceSFTPKey; k != "" {
		opts = append(opts, source.WithSFTPSourceKey(k))
	}
	return opts
}
