package secretref

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
		scheme  string
		path    string
	}{
		{name: "env", in: "env://CLOUDSTIC_PASSWORD", scheme: "env", path: "CLOUDSTIC_PASSWORD"},
		{name: "mixed case scheme", in: "KeyChain://service/account", scheme: "keychain", path: "service/account"},
		{name: "wincred", in: "WinCred://cloudstic/store/prod/password", scheme: "wincred", path: "cloudstic/store/prod/password"},
		{name: "secret service", in: "Secret-Service://cloudstic/prod/recovery-key", scheme: "secret-service", path: "cloudstic/prod/recovery-key"},
		{name: "empty", in: "", wantErr: true},
		{name: "missing separator", in: "env:CLOUDSTIC_PASSWORD", wantErr: true},
		{name: "empty path", in: "env://", wantErr: true},
		{name: "bad scheme", in: "1env://x", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q): expected error", tc.in)
				}
				var refErr *Error
				if !errors.As(err, &refErr) || refErr.Kind != KindInvalidRef {
					t.Fatalf("Parse(%q): expected invalid ref error, got %v", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.in, err)
			}
			if got.Scheme != tc.scheme || got.Path != tc.path {
				t.Fatalf("Parse(%q): got scheme=%q path=%q", tc.in, got.Scheme, got.Path)
			}
		})
	}
}

func TestResolver_Env(t *testing.T) {
	resolver := NewResolver(map[string]Backend{
		"env": NewEnvBackend(func(k string) (string, bool) {
			if k == "CLOUDSTIC_PASSWORD" {
				return "super-secret", true
			}
			return "", false
		}),
	})

	got, err := resolver.Resolve(context.Background(), "env://CLOUDSTIC_PASSWORD")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "super-secret" {
		t.Fatalf("Resolve: got %q want %q", got, "super-secret")
	}
}

func TestResolver_Errors(t *testing.T) {
	resolver := NewResolver(map[string]Backend{
		"env": NewEnvBackend(func(string) (string, bool) { return "", false }),
	})

	tests := []struct {
		name string
		ref  string
		kind ErrorKind
	}{
		{name: "invalid syntax", ref: "bad-ref", kind: KindInvalidRef},
		{name: "unsupported scheme", ref: "wincred://service/account", kind: KindBackendUnavailable},
		{name: "not found", ref: "env://MISSING", kind: KindNotFound},
		{name: "invalid env name", ref: "env://bad-name", kind: KindInvalidRef},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolver.Resolve(context.Background(), tc.ref)
			if err == nil {
				t.Fatalf("Resolve(%q): expected error", tc.ref)
			}
			var refErr *Error
			if !errors.As(err, &refErr) {
				t.Fatalf("Resolve(%q): expected *Error, got %T", tc.ref, err)
			}
			if refErr.Kind != tc.kind {
				t.Fatalf("Resolve(%q): kind=%s want=%s", tc.ref, refErr.Kind, tc.kind)
			}
		})
	}
}

func TestResolver_WritableBackendsAndStore(t *testing.T) {
	b := &testWritableBackend{scheme: "native", displayName: "Native", defaultRef: "native://cloudstic/store/prod/password"}
	resolver := NewResolver(map[string]Backend{"native": b})

	backends := resolver.WritableBackends()
	if len(backends) != 1 || backends[0].Scheme() != "native" {
		t.Fatalf("WritableBackends() = %#v", backends)
	}
	if err := resolver.Store(context.Background(), "native://cloudstic/store/prod/password", "secret"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !b.stored["native://cloudstic/store/prod/password"] {
		t.Fatal("expected backend store to be called")
	}
	if exists, err := resolver.Exists(context.Background(), "native://cloudstic/store/prod/password"); err != nil || !exists {
		t.Fatalf("Exists() = %v, %v", exists, err)
	}
}

type testWritableBackend struct {
	scheme      string
	displayName string
	defaultRef  string
	stored      map[string]bool
}

func (b *testWritableBackend) Resolve(context.Context, Ref) (string, error) { return "", nil }
func (b *testWritableBackend) Scheme() string                               { return b.scheme }
func (b *testWritableBackend) DisplayName() string                          { return b.displayName }
func (b *testWritableBackend) DefaultRef(string, string) string             { return b.defaultRef }
func (b *testWritableBackend) Exists(_ context.Context, ref Ref) (bool, error) {
	return b.stored[ref.Raw], nil
}
func (b *testWritableBackend) Store(_ context.Context, ref Ref, value string) error {
	if value == "" {
		return fmt.Errorf("empty")
	}
	if b.stored == nil {
		b.stored = map[string]bool{}
	}
	b.stored[ref.Raw] = true
	return nil
}
