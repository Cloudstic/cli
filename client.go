package cloudstic

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/repoconfig"
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/keychain"
	"github.com/cloudstic/cli/pkg/store"
)

var defaultLog = logger.New("client", logger.ColorCyan)

// ---------------------------------------------------------------------------
// client
// ---------------------------------------------------------------------------

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithReporter sets the progress reporter for the client.
func WithReporter(r Reporter) ClientOption {
	return func(c *Client) { c.reporter = r }
}

// WithEncryptionKey directly sets the AES-256-GCM encryption key (32 bytes).
// This bypasses repo config detection and unconditionally applies encryption.
// The HMAC deduplication key is automatically derived from this key.
// Use this for the SaaS product where the key is already resolved externally.
func WithEncryptionKey(key []byte) ClientOption {
	return func(c *Client) { c.encryptionKey = key }
}

// WithKeychain sets a Keychain for automatic master key resolution. During
// NewClient, the repo config is read from the store; if the repository is
// encrypted, Resolve is called to obtain the master key and the
// encryption key is derived. If the repository is not encrypted, the keychain
// is silently ignored.
func WithKeychain(kc keychain.Chain) ClientOption {
	return func(c *Client) { c.keychain = kc }
}

// WithLogger sends this client's debug output to w, along with that of the
// engine and store layers it drives.
//
// Debug output was previously reachable only by setting a package-level
// writer inside the module, so a library caller could not turn it on at all.
// A sink given here belongs to this client alone: two clients in one process
// can log to different places, or one can log while the other stays silent
// (RFC 0022 §8).
func WithLogger(w io.Writer) ClientOption {
	return func(c *Client) {
		c.logWriter = w
		c.log = defaultLog.To(w)
	}
}

// WithPackfile enables bundling small objects into 8MB packs to save API calls.
func WithPackfile(enable bool) ClientOption {
	return func(c *Client) { c.enablePackfile = enable }
}

// Client is the high-level interface for using Cloudstic as a library.
type Client struct {
	store store.ObjectStore
	// base is the raw, undecorated store. Kept so repository-level markers such
	// as the format version can be written without passing through encryption
	// or packing, which is where init writes them too.
	base store.ObjectStore
	// repoFormat is the in-process view of the on-disk format version, and the
	// single source of truth for format-dependent write policy. The compression
	// layer's frame gate reads it, and raiseRepoFormat is its only writer, so
	// framing cannot disagree with the format a mutation has stamped.
	repoFormat atomic.Int64
	// openCfg is the config NewClient read, held so the first raiseRepoFormat
	// of this client does not immediately re-read it. It is consumed on first
	// use (swapped for the noRepoConfig sentinel) and never consulted again.
	openCfg atomic.Pointer[RepoConfig]
	// repoIDCache holds the repository identifier NewClient read, including the
	// empty string for a repository that has none — nil means "not read yet",
	// which is why this is a pointer rather than a string.
	//
	// It exists so that ensureRepoID costs nothing on the overwhelmingly common
	// path where an identifier is already present. Without it, every backup,
	// prune and forget would fetch the marker again purely to learn there was
	// nothing to do.
	repoIDCache    atomic.Pointer[string]
	storedMeter    *storelayer.MeteredStore
	encryptionKey  []byte
	hmacKey        []byte
	keychain       keychain.Chain
	enablePackfile bool
	reporter       ui.Reporter
	// log is this client's debug sink, and logWriter the writer behind it,
	// kept so the engine and store layers it constructs can be given the same
	// one. An unbound logger falls back to the process-wide writer.
	log       *logger.Logger
	logWriter io.Writer
}

// engineDeps is the dependency set the engine managers are built from, as this
// client is configured. An operation that works against a different store —
// backup, which meters its own — overrides Store on the returned value.
func (c *Client) engineDeps() engine.Deps {
	return engine.Deps{
		Store:    c.store,
		Reporter: c.reporter,
		LogSink:  c.logWriter,
		HMACKey:  c.hmacKey,
		FormatV3: c.formatV3(),
	}
}

// formatV3 reports whether the repository this client opened records format v3
// (RFC 0026). It reads the same in-process format the framing gate does, so an
// engine manager cannot be built at a format the repository does not have.
func (c *Client) formatV3() bool {
	return c.repoFormat.Load() >= core.RepoFormatV3
}

func NewClient(ctx context.Context, base store.ObjectStore, opts ...ClientOption) (*Client, error) {
	c := &Client{
		base:           base,
		log:            defaultLog,
		enablePackfile: true, // Packfile is enabled by default
		reporter:       ui.NewNoOpReporter(),
	}
	for _, opt := range opts {
		opt(c)
	}

	// Read the config before anything else touches the repository, whether or
	// not a key was supplied. This carries the version gate, and gating only
	// the key-resolution path would let a caller bypass it by passing
	// WithEncryptionKey.
	cfg, encKey, err := c.openRepoConfig(ctx, base)
	if err != nil {
		return nil, err
	}
	c.encryptionKey = encKey

	// Derive HMAC dedup key from the encryption key.
	// This avoids plumbing two keys through the entire stack while
	// keeping the HMAC key cryptographically independent (HKDF is a PRF).
	if len(c.encryptionKey) > 0 {
		hmacKey, err := crypto.DeriveKey(c.encryptionKey, crypto.HKDFInfoDedupV1)
		if err != nil {
			return nil, fmt.Errorf("derive HMAC dedup key: %w", err)
		}
		c.hmacKey = hmacKey
	}

	inner := base

	// A v3 repository has no packfile layer at all (RFC 0026): every object it
	// stores is large by construction, so there is nothing to bundle and no
	// catalog to maintain. This overrides WithPackfile — the format is a
	// property of the repository, not of the caller's preference.
	v3 := cfg != nil && cfg.Version >= core.RepoFormatV3
	if v3 {
		c.enablePackfile = false
	}

	c.log.Debugf("packfile enabled: %v (format v3: %v)", c.enablePackfile, v3)
	if c.enablePackfile {
		// PackStore sits below the encryption layer, so the catalog and pack
		// footers it writes never pass through EncryptedStore. Give it its own
		// derived key so they are not left in plaintext.
		packOpts := []storelayer.PackOption{storelayer.WithPackLogger(c.logWriter)}
		if len(c.encryptionKey) > 0 {
			indexKey, err := crypto.DeriveKey(c.encryptionKey, crypto.HKDFInfoPackIndexV1)
			if err != nil {
				return nil, fmt.Errorf("derive pack index key: %w", err)
			}
			packOpts = append(packOpts, storelayer.WithPackIndexKey(indexKey))

		}

		packStore, err := storelayer.NewPackStore(inner, packOpts...)
		if err != nil {
			return nil, fmt.Errorf("init packstore: %w", err)
		}
		inner = packStore
	}

	storedMeter := storelayer.NewMeteredStore(inner)
	inner = storedMeter
	if len(c.encryptionKey) > 0 {
		inner = storelayer.NewEncryptedStore(storedMeter, c.encryptionKey)
	}

	// Seed the in-process format from disk, then let the compression layer read
	// it through the gate. Framing is thus derived from the format, not a
	// separate switch: a mutation raises the format (raiseRepoFormat) and every
	// subsequent write frames, with nothing to keep in sync. A repository below
	// core.FramedCompressionFormat starts unframed and is raised before its
	// first framed write.
	if cfg != nil {
		c.repoFormat.Store(int64(cfg.Version))
		c.openCfg.Store(cfg)
		id := cfg.ID
		c.repoIDCache.Store(&id)
	}
	c.store = storelayer.NewCompressedStore(inner, storelayer.WithFrameGate(c.framingEnabled))
	c.storedMeter = storedMeter
	return c, nil
}

// framingEnabled reports whether writes should be framed at the repository's
// current recorded format. It is the gate the compression layer consults.
func (c *Client) framingEnabled() bool {
	return c.repoFormat.Load() >= core.FramedCompressionFormat
}

// resolveKeyFromConfig uses the Keychain to resolve the master key and derive
// the encryption key, for a repository the caller has already loaded the config
// for. It takes cfg rather than re-reading it so that the version gate in
// LoadRepoConfig runs exactly once per client, on every path.
func (c *Client) resolveKeyFromSlots(ctx context.Context, base store.ObjectStore) ([]byte, error) {
	slots, err := keychain.LoadKeySlots(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("load key slots: %w", err)
	}
	masterKey, err := c.keychain.Resolve(ctx, slots)
	if err != nil {
		return nil, err
	}
	return keychain.DeriveEncryptionKey(masterKey)
}

// openRepoConfig reads the marker and resolves the encryption key in one step,
// because neither can be done first in general: a sealed marker cannot be
// decoded until the key is resolved, and whether a key is needed at all is
// what the marker says.
//
// The marker's own form breaks the cycle. Sealed means encrypted, so the key
// slots can be opened without consulting the config; plaintext means the
// config is readable directly, and its "encrypted" field then decides.
//
// An explicitly supplied key (WithEncryptionKey) is used as-is and suppresses
// slot resolution, but never the version gate.
func (c *Client) openRepoConfig(
	ctx context.Context,
	base store.ObjectStore,
) (*RepoConfig, []byte, error) {
	raw, err := fetchRepoConfigBytes(ctx, base)
	if err != nil {
		return nil, nil, err
	}
	if raw == nil {
		return nil, c.encryptionKey, nil // not initialized
	}

	key := c.encryptionKey
	sealed := repoconfig.IsSealed(raw)
	if sealed && len(key) == 0 {
		if key, err = c.resolveKeyFromSlots(ctx, base); err != nil {
			return nil, nil, err
		}
	}

	cfg, err := repoconfig.Decode(raw, key)
	if err != nil {
		return nil, nil, err
	}
	if err := checkRepoFormatSupported(cfg); err != nil {
		return nil, nil, err
	}

	if !sealed {
		// A plaintext marker claiming no encryption, on a repository that has
		// key slots, is a contradiction: the slots exist only to unwrap a key
		// this claims does not exist. Flipping that one field is otherwise the
		// cheapest way to make a client read an encrypted repository as
		// plaintext, so refuse it here — the check needs no key.
		if !cfg.Encrypted && keychain.HasKeySlots(ctx, base) {
			return nil, nil, fmt.Errorf(
				"repository config says encryption is disabled but key slots exist: " +
					"the config marker may have been modified; restore its original version",
			)
		}
		if cfg.Encrypted && len(key) == 0 {
			if key, err = c.resolveKeyFromSlots(ctx, base); err != nil {
				return nil, nil, err
			}
		}
	}

	return cfg, key, nil
}

func (c *Client) Store() store.ObjectStore { return c.store }
