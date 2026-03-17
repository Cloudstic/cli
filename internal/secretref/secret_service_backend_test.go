package secretref

import (
	"context"
	"errors"
	"testing"
)

func TestParseSecretServicePath(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		wantCollection string
		wantItem       string
		wantErr        bool
	}{
		{name: "basic", path: "cloudstic/prod/password", wantCollection: "cloudstic", wantItem: "prod/password"},
		{name: "leading slash", path: "/login/cloudstic/prod", wantCollection: "login", wantItem: "cloudstic/prod"},
		{name: "missing item", path: "cloudstic", wantErr: true},
		{name: "empty", path: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			collection, item, err := parseSecretServicePath(tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSecretServicePath(%q): expected error", tc.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSecretServicePath(%q): %v", tc.path, err)
			}
			if collection != tc.wantCollection || item != tc.wantItem {
				t.Fatalf("parseSecretServicePath(%q): collection=%q item=%q", tc.path, collection, item)
			}
		})
	}
}

func TestSecretServiceBackendResolve(t *testing.T) {
	b := newSecretServiceBackendWithLookup(func(_ context.Context, collection, item string) (string, error) {
		if collection != "cloudstic" || item != "prod/password" {
			t.Fatalf("unexpected lookup args: %q/%q", collection, item)
		}
		return "s3cr3t", nil
	})

	got, err := b.Resolve(context.Background(), Ref{Raw: "secret-service://cloudstic/prod/password", Scheme: "secret-service", Path: "cloudstic/prod/password"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "s3cr3t" {
		t.Fatalf("Resolve: got %q want s3cr3t", got)
	}
}

func TestSecretServiceBackendResolveErrors(t *testing.T) {
	tests := []struct {
		name string
		ref  Ref
		err  error
		kind ErrorKind
	}{
		{name: "invalid path", ref: Ref{Raw: "secret-service://cloudstic", Scheme: "secret-service", Path: "cloudstic"}, kind: KindInvalidRef},
		{name: "not found", ref: Ref{Raw: "secret-service://cloudstic/prod/password", Scheme: "secret-service", Path: "cloudstic/prod/password"}, err: errSecretServiceNotFound, kind: KindNotFound},
		{name: "unavailable", ref: Ref{Raw: "secret-service://cloudstic/prod/password", Scheme: "secret-service", Path: "cloudstic/prod/password"}, err: errSecretServiceUnavailable, kind: KindBackendUnavailable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newSecretServiceBackendWithLookup(func(context.Context, string, string) (string, error) {
				if tc.err == nil {
					return "", nil
				}
				return "", tc.err
			})

			_, err := b.Resolve(context.Background(), tc.ref)
			if err == nil {
				t.Fatal("expected error")
			}
			var refErr *Error
			if !errors.As(err, &refErr) {
				t.Fatalf("expected *Error, got %T", err)
			}
			if refErr.Kind != tc.kind {
				t.Fatalf("kind=%s want=%s", refErr.Kind, tc.kind)
			}
		})
	}
}

func TestSecretServiceBackend_DefaultRef(t *testing.T) {
	b := NewSecretServiceBackend()
	if got := b.DefaultRef("prod", "password"); got != "secret-service://cloudstic/prod/password" {
		t.Fatalf("DefaultRef() = %q", got)
	}
}

func TestSecretServiceBackend_Exists(t *testing.T) {
	b := newSecretServiceBackendWithFns(
		func(context.Context, string, string) (string, error) { return "", nil },
		func(_ context.Context, collection, item string) (bool, error) {
			if collection != "cloudstic" || item != "prod/password" {
				t.Fatalf("unexpected args %q/%q", collection, item)
			}
			return true, nil
		},
		nil,
	)
	exists, err := b.Exists(context.Background(), Ref{Raw: "secret-service://cloudstic/prod/password", Scheme: "secret-service", Path: "cloudstic/prod/password"})
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
}

func TestSecretServiceBackend_Store(t *testing.T) {
	b := newSecretServiceBackendWithFns(
		func(context.Context, string, string) (string, error) { return "", nil },
		nil,
		func(_ context.Context, collection, item, value string) error {
			if collection != "cloudstic" || item != "prod/password" || value != "secret" {
				t.Fatalf("unexpected args %q/%q/%q", collection, item, value)
			}
			return nil
		},
	)
	if err := b.Store(context.Background(), Ref{Raw: "secret-service://cloudstic/prod/password", Scheme: "secret-service", Path: "cloudstic/prod/password"}, "secret"); err != nil {
		t.Fatalf("Store: %v", err)
	}
}
