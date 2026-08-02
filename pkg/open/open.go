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
	"github.com/cloudstic/cli/pkg/profile"
	"github.com/cloudstic/cli/pkg/secretref"
	"github.com/cloudstic/cli/pkg/secretref/backends"
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
	secretResolver *secretref.Resolver
	promptWriter   io.Writer
	decidedConfig  config.Client
	decidedFields  config.FieldSet
}

// resolver returns the secret resolver to read profile references with,
// falling back to the built-in backend set.
//
// Defaulting is right here and wrong in pkg/config, which takes a resolver as
// a parameter precisely so it never imports the platform keychain backends. By
// the time a caller is in this package it is connecting for real and already
// links a provider SDK, so the backends cost it nothing it was not paying —
// and a one-call convenience that could not read a keychain:// reference would
// not be much of a convenience.
func (o *options) resolver() *secretref.Resolver {
	if o.secretResolver != nil {
		return o.secretResolver
	}
	return backends.NewDefaultResolver()
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

// WithSecretResolver reads the scheme://path secret references a profile names
// through r, instead of through the built-in backend set.
//
// Supply one to register a scheme the built-ins do not cover — a Vault or
// cloud-secret-manager backend — or, in a test, to resolve references without
// touching a real keychain. backends.Default returns a fresh map, so the usual
// shape is to add to it rather than replace it.
//
// This affects only the profile-reading entry points. Store, Keychain and
// Client take configuration whose secrets are already resolved.
func WithSecretResolver(r *secretref.Resolver) Option {
	return func(o *options) { o.secretResolver = r }
}

// WithDecided layers configuration of the caller's own over the profile:
// every field named in decided is taken from cfg, and the profile supplies the
// rest.
//
// This is what makes FromProfile usable by a program that has a second
// configuration mechanism — command-line flags, its own file, a form — rather
// than only by one that has nothing but the profile. Without it, such a caller
// had to abandon FromProfile and rebuild the four steps by hand, and the
// cloudstic CLI itself was the clearest example of a caller it could not serve.
//
// Pass config.FieldsSetIn(cfg) as decided when a non-empty value is what
// "I decided this" means for your mechanism. Pass an explicit
// config.NewFieldSet when empty is itself a choice you need to keep.
//
// Precedence is the same rule config.MergeProfileStore documents, including
// that a decided field's secret reference is never read — so a broken reference
// on a field you are replacing is not an error. That is the reason this is an
// option on FromProfile rather than a hook that edits the resolved
// configuration afterwards, which could not skip the resolution.
func WithDecided(cfg config.Client, decided config.FieldSet) Option {
	return func(o *options) { o.decidedConfig, o.decidedFields = cfg, decided }
}

// WithPromptWriter sets where an interactive OAuth flow writes what a human
// needs to read: that a browser is opening, and the URL to visit if it did not.
//
// Supplying it is what makes the flow usable at all when the browser cannot
// open. It is separate from WithLogger and WithDebugWriter because it is not
// diagnostics — a caller who routes it to a debug sink leaves the user staring
// at a stalled command. Without one the lines are discarded, which is right for
// a caller that has no terminal to write to.
func WithPromptWriter(w io.Writer) Option {
	return func(o *options) { o.promptWriter = w }
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
		// Unreachable: ParseStoreURI above yields one of exactly these four
		// schemes or an error, which TestParseStoreURI_SchemeIsAlwaysFromTheKnownSet
		// pins. The branch stays because each case assigns inner — without it a
		// scheme added to ParseStoreURI and forgotten here would fall through
		// with both inner and err nil, and this function would hand back a nil
		// store and no error. It reports a broken invariant rather than an
		// unsupported store, because a user cannot cause it: reaching here means
		// the two switches have drifted apart.
		return nil, fmt.Errorf("internal error: store URI %q parsed to unhandled scheme %q", cfg.URI, uri.Scheme)
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
	return openClient(ctx, cfg, newOptions(opts))
}

// FromProfile opens a repository client on the store that profile name selects
// in the profiles file at path.
//
// An empty path means the default location — profiles.yaml inside the config
// directory, honouring CLOUDSTIC_CONFIG_DIR (profile.DefaultPath) — so a
// program reading a user's profiles finds the same file the cloudstic CLI
// would, rather than a second one that silently disagrees.
//
// This is the one-call form of profile.Load → Config.StoreFor →
// config.MergeProfileStore → Client. Configuration of your own goes in through
// WithDecided, which layers it over the profile with the same precedence the
// cloudstic CLI applies to its flags. Drop to the explicit sequence when you
// need the resolved configuration itself — to display it, diff it, or decide
// something from it before connecting; that cannot be done afterwards, since
// the client this returns is already connected.
//
// A profile that names no store is an error here, because a client needs one.
// Config.StoreFor reports that case as a nil store without an error, for
// callers that have another way to reach the repository.
func FromProfile(ctx context.Context, path, name string, opts ...Option) (*cloudstic.Client, error) {
	o := newOptions(opts)

	if path == "" {
		p, err := profile.DefaultPath("")
		if err != nil {
			return nil, err
		}
		path = p
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, err
	}
	s, err := cfg.StoreFor(name)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("profile %q names no store", name)
	}
	clientCfg, err := config.MergeProfileStore(ctx, o.decidedConfig, o.decidedFields, *s, o.resolver())
	if err != nil {
		return nil, err
	}
	return openClient(ctx, clientCfg, o)
}

func openClient(ctx context.Context, cfg config.Client, o *options) (*cloudstic.Client, error) {
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
