package secretref

import (
	"context"
	"errors"
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
