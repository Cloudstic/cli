package secretref

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

type ErrorKind string

const (
	KindInvalidRef         ErrorKind = "invalid_ref"
	KindNotFound           ErrorKind = "not_found"
	KindBackendUnavailable ErrorKind = "backend_unavailable"
)

// Error is a typed secret reference error.
type Error struct {
	Kind   ErrorKind
	Ref    string
	Detail string
	Err    error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Detail == "" {
		return fmt.Sprintf("secret reference %q: %s", e.Ref, e.Kind)
	}
	return fmt.Sprintf("secret reference %q: %s (%s)", e.Ref, e.Detail, e.Kind)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func errorf(kind ErrorKind, ref, detail string, err error) *Error {
	return &Error{Kind: kind, Ref: ref, Detail: detail, Err: err}
}

var schemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

// Ref represents a parsed secret reference in the form <scheme>://<path>.
type Ref struct {
	Raw    string
	Scheme string
	Path   string
}

// Parse parses a secret reference.
func Parse(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, errorf(KindInvalidRef, raw, "empty reference; expected <scheme>://<path>", nil)
	}

	i := strings.Index(raw, "://")
	if i <= 0 {
		return Ref{}, errorf(KindInvalidRef, raw, "missing scheme separator; expected <scheme>://<path>", nil)
	}

	scheme := strings.ToLower(strings.TrimSpace(raw[:i]))
	if !schemeRe.MatchString(scheme) {
		return Ref{}, errorf(KindInvalidRef, raw, fmt.Sprintf("invalid scheme %q", scheme), nil)
	}

	path := strings.TrimSpace(raw[i+3:])
	if path == "" {
		return Ref{}, errorf(KindInvalidRef, raw, "empty path; expected <scheme>://<path>", nil)
	}

	return Ref{Raw: raw, Scheme: scheme, Path: path}, nil
}

// Backend resolves a parsed secret reference into a plaintext value.
type Backend interface {
	Resolve(ctx context.Context, ref Ref) (string, error)
}

// BlobBackend extends a backend to support binary data retrieval.
type BlobBackend interface {
	Backend
	LoadBlob(ctx context.Context, ref Ref) ([]byte, error)
}

// WritableBlobBackend extends a backend to support atomic binary data storage.
type WritableBlobBackend interface {
	BlobBackend
	SaveBlob(ctx context.Context, ref Ref, data []byte) error
	DeleteBlob(ctx context.Context, ref Ref) error
}

// WritableBackend extends a backend with native-store write and existence checks
// for interactive CLI flows.
type WritableBackend interface {
	Backend
	Scheme() string
	DisplayName() string
	WriteSupported() bool
	DefaultRef(storeName, account string) string
	Exists(ctx context.Context, ref Ref) (bool, error)
	Store(ctx context.Context, ref Ref, value string) error
}

// Resolver routes secret references by scheme.
type Resolver struct {
	backends map[string]Backend
}

// NewResolver creates a resolver from scheme backends.
func NewResolver(backends map[string]Backend) *Resolver {
	r := &Resolver{backends: map[string]Backend{}}
	for scheme, b := range backends {
		r.backends[strings.ToLower(scheme)] = b
	}
	return r
}

// NewDefaultResolver builds the baseline resolver with env:// and keychain:// support.
func NewDefaultResolver() *Resolver {
	return NewResolver(map[string]Backend{
		"env":            NewEnvBackend(nil),
		"file":           NewFileBackend(),
		"config-token":   NewConfigTokenBackend(),
		"keychain":       NewKeychainBackend(),
		"wincred":        NewWincredBackend(),
		"secret-service": NewSecretServiceBackend(),
	})
}

// Resolve parses and resolves a secret reference.
func (r *Resolver) Resolve(ctx context.Context, raw string) (string, error) {
	parsed, backend, err := r.lookupBackend(raw)
	if err != nil {
		return "", err
	}

	value, err := backend.Resolve(ctx, parsed)
	if err != nil {
		var refErr *Error
		if errors.As(err, &refErr) {
			return "", err
		}
		return "", errorf(KindBackendUnavailable, parsed.Raw, err.Error(), err)
	}
	return value, nil
}

// LoadBlob parses and retrieves a binary blob from a secret reference.
func (r *Resolver) LoadBlob(ctx context.Context, raw string) ([]byte, error) {
	parsed, backend, err := r.lookupBackend(raw)
	if err != nil {
		return nil, err
	}

	blobBackend, ok := backend.(BlobBackend)
	if !ok {
		return nil, errorf(KindBackendUnavailable, parsed.Raw, fmt.Sprintf("scheme %q does not support loading blobs", parsed.Scheme), nil)
	}

	data, err := blobBackend.LoadBlob(ctx, parsed)
	if err != nil {
		var refErr *Error
		if errors.As(err, &refErr) {
			return nil, err
		}
		return nil, errorf(KindBackendUnavailable, parsed.Raw, err.Error(), err)
	}
	return data, nil
}

// SaveBlob parses and atomically stores a binary blob to a secret reference.
func (r *Resolver) SaveBlob(ctx context.Context, raw string, data []byte) error {
	parsed, backend, err := r.lookupBackend(raw)
	if err != nil {
		return err
	}

	writable, ok := backend.(WritableBlobBackend)
	if !ok {
		return errorf(KindBackendUnavailable, parsed.Raw, fmt.Sprintf("scheme %q does not support saving blobs", parsed.Scheme), nil)
	}

	if err := writable.SaveBlob(ctx, parsed, data); err != nil {
		var refErr *Error
		if errors.As(err, &refErr) {
			return err
		}
		return errorf(KindBackendUnavailable, parsed.Raw, err.Error(), err)
	}
	return nil
}

// DeleteBlob parses and removes a binary blob from a secret reference.
func (r *Resolver) DeleteBlob(ctx context.Context, raw string) error {
	parsed, backend, err := r.lookupBackend(raw)
	if err != nil {
		return err
	}

	writable, ok := backend.(WritableBlobBackend)
	if !ok {
		return errorf(KindBackendUnavailable, parsed.Raw, fmt.Sprintf("scheme %q does not support deleting blobs", parsed.Scheme), nil)
	}

	if err := writable.DeleteBlob(ctx, parsed); err != nil {
		var refErr *Error
		if errors.As(err, &refErr) {
			return err
		}
		return errorf(KindBackendUnavailable, parsed.Raw, err.Error(), err)
	}
	return nil
}

// Exists reports whether a writable secret reference already exists.
func (r *Resolver) Exists(ctx context.Context, raw string) (bool, error) {
	parsed, writable, err := r.lookupWritableBackend(raw)
	if err != nil {
		return false, err
	}

	exists, err := writable.Exists(ctx, parsed)
	if err != nil {
		var refErr *Error
		if errors.As(err, &refErr) {
			return false, err
		}
		return false, errorf(KindBackendUnavailable, parsed.Raw, err.Error(), err)
	}
	return exists, nil
}

// Store writes a secret value through a writable backend.
func (r *Resolver) Store(ctx context.Context, raw, value string) error {
	parsed, writable, err := r.lookupWritableBackend(raw)
	if err != nil {
		return err
	}

	if err := writable.Store(ctx, parsed, value); err != nil {
		var refErr *Error
		if errors.As(err, &refErr) {
			return err
		}
		return errorf(KindBackendUnavailable, parsed.Raw, err.Error(), err)
	}
	return nil
}

// WritableBackends returns registered backends that support interactive writes.
func (r *Resolver) WritableBackends() []WritableBackend {
	backends := make([]WritableBackend, 0, len(r.backends))
	for _, backend := range r.backends {
		if writable, ok := backend.(WritableBackend); ok {
			if writable.WriteSupported() {
				backends = append(backends, writable)
			}
		}
	}
	slices.SortFunc(backends, func(a, b WritableBackend) int {
		return strings.Compare(a.Scheme(), b.Scheme())
	})
	return backends
}

func (r *Resolver) lookupBackend(raw string) (Ref, Backend, error) {
	parsed, err := Parse(raw)
	if err != nil {
		return Ref{}, nil, err
	}
	backend, ok := r.backends[parsed.Scheme]
	if !ok {
		return Ref{}, nil, errorf(KindBackendUnavailable, parsed.Raw, fmt.Sprintf("no backend registered for scheme %q", parsed.Scheme), nil)
	}
	return parsed, backend, nil
}

func (r *Resolver) lookupWritableBackend(raw string) (Ref, WritableBackend, error) {
	parsed, backend, err := r.lookupBackend(raw)
	if err != nil {
		return Ref{}, nil, err
	}
	writable, ok := backend.(WritableBackend)
	if !ok {
		return Ref{}, nil, errorf(KindBackendUnavailable, parsed.Raw, fmt.Sprintf("scheme %q does not support writing secrets", parsed.Scheme), nil)
	}
	return parsed, writable, nil
}

type EnvLookup func(string) (string, bool)

// EnvBackend resolves env://VAR references.
type EnvBackend struct {
	lookup EnvLookup
}

func NewEnvBackend(lookup EnvLookup) *EnvBackend {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &EnvBackend{lookup: lookup}
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (b *EnvBackend) Resolve(_ context.Context, ref Ref) (string, error) {
	name := strings.TrimSpace(ref.Path)
	if strings.HasPrefix(name, "/") {
		name = strings.TrimLeft(name, "/")
	}
	if name == "" || !envNameRe.MatchString(name) {
		return "", errorf(KindInvalidRef, ref.Raw, "invalid env variable name in env:// reference", nil)
	}

	value, ok := b.lookup(name)
	if !ok {
		return "", errorf(KindNotFound, ref.Raw, fmt.Sprintf("environment variable %q is not set", name), nil)
	}
	return value, nil
}
