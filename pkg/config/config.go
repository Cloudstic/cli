// Package config holds the resolved configuration for opening a Cloudstic
// repository: which store to talk to, which credentials unlock it, and how the
// client should behave.
//
// It is the boundary between how configuration is *expressed* — command-line
// flags, environment variables, a profiles YAML file — and how a repository is
// *opened*. Values here are fully resolved: a store URI has been chosen, and
// secret references have already been read into the credentials they name.
// Nothing in this package performs I/O against a store or a cloud provider;
// pkg/open does that, and takes these values as input.
//
// The split matters for what it costs to import. This package depends only on
// pkg/profile and pkg/secretref, so reading a profiles file and resolving it
// into a configuration — to validate it, display it, or hand it to something
// else — pulls in no cloud SDK. Constructing a store from that configuration
// necessarily does, which is why construction lives in pkg/open (RFC 0022 §7).
//
// Zero values are the correct defaults. A Client{Store: …} with nothing else
// set behaves the way the cloudstic CLI behaves with no flags passed, which is
// why packfiles are expressed as DisablePackfile rather than Packfile: the
// client enables them by default, so a positive field would silently turn them
// off for every caller who did not think to mention them.
package config

// S3 holds the credentials and endpoint settings for an S3 store.
type S3 struct {
	Endpoint  string
	Region    string
	Profile   string
	AccessKey string
	SecretKey string
}

// B2 holds Backblaze B2 application key credentials.
type B2 struct {
	KeyID  string
	AppKey string
}

// SFTP holds SFTP authentication and host-key settings. The same shape serves
// both a store and a backup source, which are configured independently.
type SFTP struct {
	Password   string
	Key        string
	KnownHosts string
	Insecure   bool
}

// KMS holds AWS KMS settings for kms-platform key slots.
type KMS struct {
	KeyARN   string
	Region   string
	Endpoint string
}

// Store is everything needed to construct an object store.
//
// Not to be confused with profile.Store, which is the same configuration as
// *declared* in a profiles file, secret references and all. Store is what that
// resolves to. config.FromProfileStore converts one into the other.
type Store struct {
	URI   string
	S3    S3
	B2    B2
	SFTP  SFTP
	Debug bool
}

// Unlock is everything needed to build the keychain that unlocks a repository.
//
// The credentials are tried in a fixed order — KMS, then EncryptionKey, then
// Password, then RecoveryKey — so supplying more than one is not ambiguous.
// Prompt and NoPrompt govern the interactive fallback, which is only ever
// reachable when the caller opts into it.
type Unlock struct {
	Password      string
	EncryptionKey string
	RecoveryKey   string
	KMS           KMS
	Prompt        bool
	NoPrompt      bool
}

// Client is the resolved configuration for opening a repository client.
type Client struct {
	Store           Store
	Unlock          Unlock
	DisablePackfile bool
	Quiet           bool
	JSON            bool

	// ObjectCacheDir is where repeated object reads are cached on local disk.
	// Empty disables the cache.
	//
	// Deliberately not a profile field, unlike everything in Store: a cache
	// directory is a property of the machine running the backup, not of the
	// repository being backed up, so it has no business travelling in a
	// profiles file that may be shared or committed.
	ObjectCacheDir string

	// ObjectCacheBytes bounds that directory. Zero takes the built-in default;
	// there is no value meaning "no limit".
	ObjectCacheBytes int64

	// DisableObjectCache turns the cache off whatever ObjectCacheDir says, so
	// an explicit flag can override a directory inherited from the
	// environment. Expressed as the negative for the reason given above: the
	// zero value is the default behaviour.
	DisableObjectCache bool

	// Verbose asks the reporter for per-item detail. It is a presentation
	// choice, which is why it lives here and on the reporter rather than as an
	// option on each operation.
	Verbose bool
}
