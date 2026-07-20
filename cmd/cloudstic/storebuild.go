package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
	"golang.org/x/crypto/ssh"
)

// This file builds an object store from resolved configuration. It takes a
// storeConfig value rather than parsed flags, so store construction is
// testable without going through the flag package. Client construction (which
// layers a store, keychain, and reporter together) lives in clientbuild.go.

// openStore constructs the object store described by cfg, with debug wrapping
// applied. Used by commands that operate on the store directly (init, key).
func openStore(ctx context.Context, cfg storeConfig) (store.ObjectStore, error) {
	raw, err := newObjectStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s, _ := withDebugStore(raw, cfg.debug)
	return s, nil
}

// withDebugStore wraps a store with a DebugStore and enables the global debug
// logger when debug is set. It returns the (possibly wrapped) store and the log
// writer, which the console reporter shares so store logs and progress output
// do not interleave.
func withDebugStore(s store.ObjectStore, debug bool) (store.ObjectStore, *ui.SafeLogWriter) {
	if !debug {
		return s, nil
	}
	log := &ui.SafeLogWriter{}
	logger.Writer = log
	return store.NewDebugStore(s, log), log
}

// newObjectStore constructs the backend store named by the configured URI,
// without any decorator layers.
func newObjectStore(ctx context.Context, cfg storeConfig) (store.ObjectStore, error) {
	uri, err := parseStoreURI(cfg.uri)
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
			ctx,
			uri.bucket,
			store.WithS3Endpoint(cfg.s3.endpoint),
			store.WithS3Region(cfg.s3.region),
			store.WithS3Profile(cfg.s3.profile),
			store.WithS3Credentials(cfg.s3.accessKey, cfg.s3.secretKey),
			store.WithS3Prefix(uri.prefix),
		)
	case "sftp":
		inner, err = store.NewSFTPStore(uri.host, sftpStoreOpts(cfg.sftp, uri)...)
	default:
		return nil, fmt.Errorf("unsupported store type: %s", uri.scheme)
	}

	if err != nil {
		return nil, err
	}
	return inner, nil
}

func sftpStoreOpts(cfg sftpConfig, uri *storeURIParts) []store.SFTPStoreOption {
	opts := []store.SFTPStoreOption{
		store.WithSFTPBasePath(uri.path),
	}
	if uri.port != "" {
		opts = append(opts, store.WithSFTPPort(uri.port))
	}
	if uri.user != "" {
		opts = append(opts, store.WithSFTPUser(uri.user))
	}
	if cfg.password != "" {
		opts = append(opts, store.WithSFTPPassword(cfg.password))
	}
	if cfg.key != "" {
		opts = append(opts, store.WithSFTPKey(cfg.key))
	}
	if cfg.insecure {
		opts = append(opts, store.WithSFTPHostKeyCallback(ssh.InsecureIgnoreHostKey())) //nolint:gosec // explicitly requested by user
	}
	if cfg.knownHosts != "" {
		opts = append(opts, store.WithSFTPKnownHosts(cfg.knownHosts))
	}
	return opts
}
