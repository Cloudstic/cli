package secretref

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
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

// NewError builds a typed secret-reference error. Backend implementations
// outside this package use it to report failures in the same shape the
// built-in backends do, so callers can branch on Kind regardless of which
// backend produced the error.
func NewError(kind ErrorKind, ref, detail string, err error) *Error {
	return errorf(kind, ref, detail, err)
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
	Scheme() string
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
//
// Resolutions from interactive native stores (macOS Keychain, Windows
// Credential Manager, Linux Secret Service) are cached in-process and
// deduplicated with singleflight. This matters for unsigned/dev builds, where
// the OS cannot persist an "always allow" ACL keyed to the binary's code
// signature and so re-prompts on every keychain access: without the cache, the
// concurrent store probes and per-action re-probes would each pop a separate
// password dialog. With it, a given reference prompts at most once per process.
type Resolver struct {
	backends map[string]Backend

	mu    sync.Mutex
	cache map[string]string
	group singleflight.Group
}

// NewResolver creates a resolver from scheme backends.
func NewResolver(backends map[string]Backend) *Resolver {
	r := &Resolver{backends: map[string]Backend{}, cache: map[string]string{}}
	for scheme, b := range backends {
		r.backends[strings.ToLower(scheme)] = b
	}
	return r
}

// cachedScheme reports whether a scheme's resolutions should be cached. Only
// the interactive native stores are cached; env/file/config-token resolution is
// cheap, silent, and may legitimately change within a process.
func cachedScheme(scheme string) bool {
	switch scheme {
	case "keychain", "wincred", "secret-service":
		return true
	}
	return false
}

func (r *Resolver) cacheGet(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.cache[key]
	return v, ok
}

func (r *Resolver) cacheSet(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = value
}

func (r *Resolver) cacheInvalidate(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, key)
}

// Resolve parses and resolves a secret reference.
func (r *Resolver) Resolve(ctx context.Context, raw string) (string, error) {
	parsed, backend, err := r.lookupBackend(raw)
	if err != nil {
		return "", err
	}
	if cachedScheme(parsed.Scheme) {
		return r.resolveCached(ctx, parsed, backend)
	}
	return resolveBackend(ctx, parsed, backend)
}

// resolveCached serves a cacheable resolution from the in-process cache, or
// performs exactly one backend call for concurrent callers via singleflight and
// caches the successful result. Errors are never cached, so a denied or failed
// prompt can be retried.
func (r *Resolver) resolveCached(ctx context.Context, parsed Ref, backend Backend) (string, error) {
	key := parsed.Raw
	if v, ok := r.cacheGet(key); ok {
		return v, nil
	}
	v, err, _ := r.group.Do(key, func() (any, error) {
		if v, ok := r.cacheGet(key); ok {
			return v, nil
		}
		value, err := resolveBackend(ctx, parsed, backend)
		if err != nil {
			return "", err
		}
		r.cacheSet(key, value)
		return value, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func resolveBackend(ctx context.Context, parsed Ref, backend Backend) (string, error) {
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
	if cachedScheme(parsed.Scheme) {
		r.cacheSet(parsed.Raw, string(data))
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
	r.cacheInvalidate(parsed.Raw)
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
	if cachedScheme(parsed.Scheme) {
		r.cacheSet(parsed.Raw, value)
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
