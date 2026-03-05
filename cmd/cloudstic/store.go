package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/logger"
	intsftp "github.com/cloudstic/cli/internal/sftp"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/moby/term"
)

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

	kp, err := g.buildKeyProvider(context.Background())
	if err != nil {
		return nil, err
	}

	return cloudstic.NewClient(raw,
		cloudstic.WithKeyProvider(kp),
		cloudstic.WithReporter(reporter),
		cloudstic.WithPackfile(packfileEnabled),
	)
}

// buildKMSClient creates an AWS KMS client if -kms-key-arn is set, otherwise
// returns nil. The returned client implements both KMSEncrypter and KMSDecrypter.
func (g *globalFlags) buildKMSClient(ctx context.Context) (*crypto.AWSKMSClient, error) {
	if g.kmsKeyARN == nil || *g.kmsKeyARN == "" {
		return nil, nil
	}
	client, err := crypto.NewAWSKMSDecrypter(ctx)
	if err != nil {
		return nil, fmt.Errorf("init KMS client: %w", err)
	}
	return client, nil
}

// buildKeyProvider constructs a Credentials key provider from the CLI flags.
// The returned provider always attempts auto-detection: the client reads the
// repo config and only calls ResolveKey when encryption is enabled.
func (g *globalFlags) buildKeyProvider(ctx context.Context) (cloudstic.KeyProvider, error) {
	platformKey, err := g.parsePlatformKey()
	if err != nil {
		return nil, err
	}

	kmsClient, err := g.buildKMSClient(ctx)
	if err != nil {
		return nil, err
	}

	// Avoid assigning a typed nil *AWSKMSClient to the interface field;
	// a non-nil interface wrapping a nil pointer would bypass nil checks.
	var kmsDecrypter cloudstic.KMSDecrypter
	if kmsClient != nil {
		kmsDecrypter = kmsClient
	}

	var passwordPrompt func() (string, error)
	if term.IsTerminal(os.Stdin.Fd()) {
		passwordPrompt = func() (string, error) {
			return ui.PromptPassword("Repository password")
		}
	}

	return &cloudstic.Credentials{
		PlatformKey:      platformKey,
		Password:         *g.encryptionPassword,
		RecoveryMnemonic: *g.recoveryKey,
		KMSDecrypter:     kmsDecrypter,
		PasswordPrompt:   passwordPrompt,
	}, nil
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
		inner, err = store.NewB2StoreWithPrefix(keyID, appKey, *g.storePath, *g.storePrefix)
	case "s3":
		if *g.storePath == "" {
			return nil, fmt.Errorf("-store-path must be set to the S3 bucket name")
		}
		inner, err = store.NewS3Store(context.Background(), *g.s3Endpoint, *g.s3Region, *g.storePath, *g.s3AccessKey, *g.s3SecretKey, *g.storePrefix)
	case "sftp":
		cfg, sftpErr := g.sftpConfig(g.storeSFTPHost, g.storeSFTPPort, g.storeSFTPUser, g.storeSFTPPassword, g.storeSFTPKey, g.storePath)
		if sftpErr != nil {
			return nil, sftpErr
		}
		inner, err = store.NewSFTPStore(cfg)
	default:
		return nil, fmt.Errorf("unsupported store type: %s", *g.storeType)
	}

	if err != nil {
		return nil, err
	}

	return inner, nil
}

func (g *globalFlags) sftpConfig(host, port, user, pass, key, path *string) (intsftp.Config, error) {
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
		return intsftp.Config{}, fmt.Errorf("--sftp-host (or CLOUDSTIC_SFTP_HOST) is required for sftp")
	}
	if u == "" {
		return intsftp.Config{}, fmt.Errorf("--sftp-user (or CLOUDSTIC_SFTP_USER) is required for sftp")
	}

	return intsftp.Config{
		Host:           h,
		Port:           p,
		User:           u,
		Password:       pw,
		PrivateKeyPath: k,
		BasePath:       bp,
	}, nil
}
