package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

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

func (g *globalFlags) openClient() (*cloudstic.Client, error) {
	raw, err := g.initObjectStore()
	if err != nil {
		return nil, err
	}
	raw = g.applyDebug(raw)

	packfileEnabled := g.enablePackfile != nil && *g.enablePackfile

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

	kc, err := g.buildKeychain(context.Background())
	if err != nil {
		return nil, err
	}

	return cloudstic.NewClient(raw,
		cloudstic.WithKeychain(kc),
		cloudstic.WithReporter(reporter),
		cloudstic.WithPackfile(packfileEnabled),
	)
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
	if *g.encryptionPassword != "" {
		chain = append(chain, keychain.WithPassword(*g.encryptionPassword))
	}
	if *g.recoveryKey != "" {
		chain = append(chain, keychain.WithRecoveryKey(*g.recoveryKey))
	}
	promptRequested := g.password != nil && *g.password
	if (len(chain) == 0 || promptRequested) && term.IsTerminal(os.Stdin.Fd()) {
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

func (g *globalFlags) initObjectStore() (store.ObjectStore, error) {
	var inner store.ObjectStore
	var err error

	switch *g.storeType {
	case "local":
		inner, err = store.NewLocalStore(*g.storePath)
	case "b2":
		keyID := os.Getenv("B2_KEY_ID")
		appKey := os.Getenv("B2_APP_KEY")
		if keyID == "" || appKey == "" {
			return nil, fmt.Errorf("B2_KEY_ID and B2_APP_KEY env vars required for b2 store")
		}
		inner, err = store.NewB2Store(*g.storePath, store.WithCredentials(keyID, appKey), store.WithPrefix(*g.storePrefix))
	case "s3":
		if *g.storePath == "" {
			return nil, fmt.Errorf("-store-path must be set to the S3 bucket name")
		}
		inner, err = store.NewS3Store(
			context.Background(),
			*g.storePath,
			store.WithS3Endpoint(*g.s3Endpoint),
			store.WithS3Region(*g.s3Region),
			store.WithS3Credentials(*g.s3AccessKey, *g.s3SecretKey),
			store.WithS3Prefix(*g.storePrefix),
		)
	case "sftp":
		sftpHost, sftpOpts := g.sftpStoreOpts(g.storeSFTPHost, g.storeSFTPPort, g.storeSFTPUser, g.storeSFTPPassword, g.storeSFTPKey, g.storePath)
		if sftpHost == "" {
			return nil, fmt.Errorf("--sftp-host is required for sftp store")
		}
		inner, err = store.NewSFTPStore(sftpHost, sftpOpts...)
	default:
		return nil, fmt.Errorf("unsupported store type: %s", *g.storeType)
	}

	if err != nil {
		return nil, err
	}

	return inner, nil
}

func (g *globalFlags) sftpStoreOpts(host, port, user, pass, key, path *string) (string, []store.SFTPStoreOption) {
	h := *host
	if h == "" {
		h = *g.sftpHost
	}
	p := *port
	if p == "" {
		p = *g.sftpPort
	}
	u := *user
	if u == "" {
		u = *g.sftpUser
	}
	pw := *pass
	if pw == "" {
		pw = *g.sftpPassword
	}
	k := *key
	if k == "" {
		k = *g.sftpKey
	}
	bp := *path

	if h == "" {
		return "", nil
	}

	opts := []store.SFTPStoreOption{
		store.WithSFTPBasePath(bp),
	}
	if p != "" {
		opts = append(opts, store.WithSFTPPort(p))
	}
	if u != "" {
		opts = append(opts, store.WithSFTPUser(u))
	}
	if pw != "" {
		opts = append(opts, store.WithSFTPPassword(pw))
	}
	if k != "" {
		opts = append(opts, store.WithSFTPKey(k))
	}
	return h, opts
}

func (g *globalFlags) sftpSourceOpts(host, port, user, pass, key, path *string) (string, []source.SFTPOption) {
	h := *host
	if h == "" {
		h = *g.sftpHost
	}
	p := *port
	if p == "" {
		p = *g.sftpPort
	}
	u := *user
	if u == "" {
		u = *g.sftpUser
	}
	pw := *pass
	if pw == "" {
		pw = *g.sftpPassword
	}
	k := *key
	if k == "" {
		k = *g.sftpKey
	}
	bp := *path

	if h == "" {
		return "", nil
	}

	opts := []source.SFTPOption{
		source.WithSFTPSourceBasePath(bp),
	}
	if p != "" {
		opts = append(opts, source.WithSFTPSourcePort(p))
	}
	if u != "" {
		opts = append(opts, source.WithSFTPSourceUser(u))
	}
	if pw != "" {
		opts = append(opts, source.WithSFTPSourcePassword(pw))
	}
	if k != "" {
		opts = append(opts, source.WithSFTPSourceKey(k))
	}
	return h, opts
}
