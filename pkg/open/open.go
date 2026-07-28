// Package open constructs live objects from resolved configuration: an object
// store from a store URI and its credentials, a keychain from a set of unlock
// credentials, and a repository client from both.
//
// It is the other half of pkg/config. That package answers "what did the user
// configure"; this one answers "connect to it". The split is what decides
// import cost: pkg/config depends on nothing heavier than YAML parsing, so
// reading and validating a profiles file is cheap, while constructing an S3 or
// KMS client necessarily links a cloud SDK and only a caller who does that
// pays for it (RFC 0022 §7).
//
// Everything the constructors need from the outside world is passed in rather
// than reached for. The debug sink is a writer the caller supplies, the
// progress reporter is a value the caller chooses, and whether an interactive
// password prompt is available at all is the caller's decision — this package
// never inspects the process's own stdin, because a library's answer to "can I
// prompt?" is not the same as a terminal program's.
package open

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/store"
	b2store "github.com/cloudstic/cli/pkg/store/b2"
	localstore "github.com/cloudstic/cli/pkg/store/local"
	s3store "github.com/cloudstic/cli/pkg/store/s3"
	sftpstore "github.com/cloudstic/cli/pkg/store/sftp"
)

// defaultS3Region is the region used when the configuration names none. It is
// applied here, at the single point of construction, so that a configuration
// built from a profile and one built from command-line flags cannot disagree
// about it.
const defaultS3Region = "us-east-1"

// Option configures how a store, keychain, or client is opened.
//
// These are behaviour knobs supplied by the caller, deliberately separate from
// the pkg/config value types: configuration describes a repository and can be
// serialized, compared, and round-tripped, while these carry writers,
// callbacks, and interfaces that cannot.
type Option func(*options)

type options struct {
	debugWriter    io.Writer
	logWriter      io.Writer
	backendWrapper func(store.ObjectStore) (store.ObjectStore, error)
	reporter       cloudstic.Reporter
	promptResolve  func() (string, error)
	promptWrap     func() (string, error)
}

func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// WithDebugWriter logs every store operation to w.
//
// The writer is supplied rather than created here because it is usually shared
// with a progress reporter, so that operation logs and progress output do not
// interleave.
func WithDebugWriter(w io.Writer) Option {
	return func(o *options) { o.debugWriter = w }
}

// WithLogger sends the client's debug output — its own and that of the engine
// and store layers it drives — to w.
//
// This is distinct from WithDebugWriter, which traces individual store
// operations. A caller that wants both passes both; they are separate because
// per-operation tracing is far noisier than the component diagnostics, and is
// often wanted on its own.
func WithLogger(w io.Writer) Option {
	return func(o *options) { o.logWriter = w }
}

// WithBackendWrapper wraps the constructed backend before the repository
// layers are applied on top of it, for a caller adding its own quota,
// rate-limit, metrics, or fault-injection decorator.
//
// This is the supported place to intervene in the store chain. The repository
// decorators themselves stay internal because their composition order is a
// correctness and security invariant (RFC 0022 §4a); wrapping the backend
// underneath them carries no such hazard.
func WithBackendWrapper(fn func(store.ObjectStore) (store.ObjectStore, error)) Option {
	return func(o *options) { o.backendWrapper = fn }
}

// WithReporter sets the progress reporter the client reports through. Without
// one the client reports nothing.
func WithReporter(r cloudstic.Reporter) Option {
	return func(o *options) { o.reporter = r }
}

// WithPasswordPrompt makes an interactive password prompt available as a
// last-resort credential, used only when the configuration leaves no other way
// in (or asks for a prompt explicitly) and does not forbid prompting.
//
// resolve is called to unlock an existing repository; wrap is called to choose
// a password for a new key slot, and should confirm it.
//
// Supplying these is what makes prompting possible at all. That is the
// caller's decision to make: this package cannot tell whether a prompt would
// reach a human, and guessing by inspecting os.Stdin would be wrong for every
// caller that is not a terminal program.
func WithPasswordPrompt(resolve, wrap func() (string, error)) Option {
	return func(o *options) { o.promptResolve, o.promptWrap = resolve, wrap }
}

// Store constructs the object store described by cfg.
//
// The result is a raw backend, with no repository layers on it. Pass it to
// cloudstic.NewClient — or to Client below, which does that — to get
// compression, encryption, and packing.
func Store(ctx context.Context, cfg config.Store, opts ...Option) (store.ObjectStore, error) {
	return openStore(ctx, cfg, newOptions(opts))
}

func openStore(ctx context.Context, cfg config.Store, o *options) (store.ObjectStore, error) {
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

	if o.backendWrapper != nil {
		if inner, err = o.backendWrapper(inner); err != nil {
			return nil, err
		}
	}
	if o.debugWriter != nil {
		inner = store.NewDebugStore(inner, o.debugWriter)
	}
	return inner, nil
}

// s3Region applies the built-in region default.
func s3Region(region string) string {
	if region == "" {
		return defaultS3Region
	}
	return region
}

func sftpStoreOpts(cfg config.SFTP, uri *config.StoreURI) []sftpstore.Option {
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
		opts = append(opts, sftpstore.WithHostKeyCallback(ssh.InsecureIgnoreHostKey())) //nolint:gosec // explicitly requested by the caller
	}
	if cfg.KnownHosts != "" {
		opts = append(opts, sftpstore.WithKnownHosts(cfg.KnownHosts))
	}
	return opts
}

// Client opens a repository client on the store described by cfg, unlocked
// with the credentials cfg names.
func Client(ctx context.Context, cfg config.Client, opts ...Option) (*cloudstic.Client, error) {
	o := newOptions(opts)

	raw, err := openStore(ctx, cfg.Store, o)
	if err != nil {
		return nil, err
	}
	kc, err := openKeychain(ctx, cfg.Unlock, o)
	if err != nil {
		return nil, err
	}

	clientOpts := []cloudstic.ClientOption{
		cloudstic.WithKeychain(kc),
		cloudstic.WithPackfile(!cfg.DisablePackfile),
	}
	if o.logWriter != nil {
		clientOpts = append(clientOpts, cloudstic.WithLogger(o.logWriter))
	}
	if o.reporter != nil {
		clientOpts = append(clientOpts, cloudstic.WithReporter(o.reporter))
	}
	return cloudstic.NewClient(ctx, raw, clientOpts...)
}
