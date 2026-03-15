package secretref

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
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

// NewDefaultResolver builds the baseline resolver with env:// support.
func NewDefaultResolver() *Resolver {
	return NewResolver(map[string]Backend{
		"env":      NewEnvBackend(nil),
		"keychain": NewKeychainBackend(),
	})
}

// Resolve parses and resolves a secret reference.
func (r *Resolver) Resolve(ctx context.Context, raw string) (string, error) {
	parsed, err := Parse(raw)
	if err != nil {
		return "", err
	}
	backend, ok := r.backends[parsed.Scheme]
	if !ok {
		return "", errorf(KindBackendUnavailable, parsed.Raw, fmt.Sprintf("no backend registered for scheme %q", parsed.Scheme), nil)
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
