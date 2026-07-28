package main

import (
	"context"
	"fmt"

	"golang.org/x/crypto/ssh"

	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
	b2store "github.com/cloudstic/cli/pkg/store/b2"
	localstore "github.com/cloudstic/cli/pkg/store/local"
	s3store "github.com/cloudstic/cli/pkg/store/s3"
	sftpstore "github.com/cloudstic/cli/pkg/store/sftp"
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
		inner, err = localstore.New(uri.path)
	case "b2":
		if cfg.b2.keyID == "" || cfg.b2.appKey == "" {
			return nil, fmt.Errorf("B2 credentials required: pass -b2-key-id/-b2-app-key (or set B2_KEY_ID/B2_APP_KEY)")
		}
		inner, err = b2store.New(uri.bucket, b2store.WithCredentials(cfg.b2.keyID, cfg.b2.appKey), b2store.WithPrefix(uri.prefix))
	case "s3":
		inner, err = s3store.New(
			ctx,
			uri.bucket,
			s3store.WithEndpoint(cfg.s3.endpoint),
			s3store.WithRegion(s3Region(cfg.s3.region)),
			s3store.WithProfile(cfg.s3.profile),
			s3store.WithCredentials(cfg.s3.accessKey, cfg.s3.secretKey),
			s3store.WithPrefix(uri.prefix),
		)
	case "sftp":
		inner, err = sftpstore.New(uri.host, sftpStoreOpts(cfg.sftp, uri)...)
	default:
		return nil, fmt.Errorf("unsupported store type: %s", uri.scheme)
	}

	if err != nil {
		return nil, err
	}
	return withCrashInjection(inner)
}

// s3Region applies the built-in region default. It lives here, at the single
// point of construction, rather than being pre-filled into every storeConfig
// that might reach it: the flag path gets defaultS3Region from the -s3-region
// flag's own default, but a config built from a profile store has no flag to
// carry it, and prefilling that separately is how the two paths drift.
func s3Region(region string) string {
	if region == "" {
		return defaultS3Region
	}
	return region
}

func sftpStoreOpts(cfg sftpConfig, uri *storeURIParts) []sftpstore.Option {
	opts := []sftpstore.Option{
		sftpstore.WithBasePath(uri.path),
	}
	if uri.port != "" {
		opts = append(opts, sftpstore.WithPort(uri.port))
	}
	if uri.user != "" {
		opts = append(opts, sftpstore.WithUser(uri.user))
	}
	if cfg.password != "" {
		opts = append(opts, sftpstore.WithPassword(cfg.password))
	}
	if cfg.key != "" {
		opts = append(opts, sftpstore.WithKey(cfg.key))
	}
	if cfg.insecure {
		opts = append(opts, sftpstore.WithHostKeyCallback(ssh.InsecureIgnoreHostKey())) //nolint:gosec // explicitly requested by user
	}
	if cfg.knownHosts != "" {
		opts = append(opts, sftpstore.WithKnownHosts(cfg.knownHosts))
	}
	return opts
}
