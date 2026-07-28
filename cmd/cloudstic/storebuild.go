package main

import (
	"context"
	"fmt"
	"github.com/cloudstic/cli/pkg/config"

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
	s, _ := withDebugStore(raw, cfg.Debug)
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
	uri, err := config.ParseStoreURI(cfg.URI)
	if err != nil {
		return nil, err
	}

	var inner store.ObjectStore
	switch uri.Scheme {
	case "local":
		inner, err = localstore.New(uri.Path)
	case "b2":
		if cfg.B2.KeyID == "" || cfg.B2.AppKey == "" {
			return nil, fmt.Errorf("B2 credentials required: pass -b2-key-id/-b2-app-key (or set B2_KEY_ID/B2_APP_KEY)")
		}
		inner, err = b2store.New(uri.Bucket, b2store.WithCredentials(cfg.B2.KeyID, cfg.B2.AppKey), b2store.WithPrefix(uri.Prefix))
	case "s3":
		inner, err = s3store.New(
			ctx,
			uri.Bucket,
			s3store.WithEndpoint(cfg.S3.Endpoint),
			s3store.WithRegion(s3Region(cfg.S3.Region)),
			s3store.WithProfile(cfg.S3.Profile),
			s3store.WithCredentials(cfg.S3.AccessKey, cfg.S3.SecretKey),
			s3store.WithPrefix(uri.Prefix),
		)
	case "sftp":
		inner, err = sftpstore.New(uri.Host, sftpStoreOpts(cfg.SFTP, uri)...)
	default:
		return nil, fmt.Errorf("unsupported store type: %s", uri.Scheme)
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
		sftpstore.WithBasePath(uri.Path),
	}
	if uri.Port != "" {
		opts = append(opts, sftpstore.WithPort(uri.Port))
	}
	if uri.User != "" {
		opts = append(opts, sftpstore.WithUser(uri.User))
	}
	if cfg.Password != "" {
		opts = append(opts, sftpstore.WithPassword(cfg.Password))
	}
	if cfg.Key != "" {
		opts = append(opts, sftpstore.WithKey(cfg.Key))
	}
	if cfg.Insecure {
		opts = append(opts, sftpstore.WithHostKeyCallback(ssh.InsecureIgnoreHostKey())) //nolint:gosec // explicitly requested by user
	}
	if cfg.KnownHosts != "" {
		opts = append(opts, sftpstore.WithKnownHosts(cfg.KnownHosts))
	}
	return opts
}
