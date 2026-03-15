package secretref

import (
	"context"
	"errors"
	"testing"
)

func TestParseKeychainPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantErr  bool
		wantSvc  string
		wantAcct string
	}{
		{name: "basic", path: "cloudstic/prod/password", wantSvc: "cloudstic/prod", wantAcct: "password"},
		{name: "leading slash", path: "/cloudstic/prod/password", wantSvc: "cloudstic/prod", wantAcct: "password"},
		{name: "missing separator", path: "cloudstic", wantErr: true},
		{name: "empty account", path: "cloudstic/", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, acct, err := parseKeychainPath(tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseKeychainPath(%q): expected error", tc.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseKeychainPath(%q): %v", tc.path, err)
			}
			if svc != tc.wantSvc || acct != tc.wantAcct {
				t.Fatalf("parseKeychainPath(%q): service=%q account=%q", tc.path, svc, acct)
			}
		})
	}
}

func TestKeychainBackend_Resolve(t *testing.T) {
	b := newKeychainBackendWithLookup(func(_ context.Context, service, account string) (string, error) {
		if service != "cloudstic/prod" || account != "password" {
			t.Fatalf("unexpected lookup args: %q/%q", service, account)
		}
		return "s3cr3t", nil
	})

	got, err := b.Resolve(context.Background(), Ref{Raw: "keychain://cloudstic/prod/password", Scheme: "keychain", Path: "cloudstic/prod/password"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "s3cr3t" {
		t.Fatalf("Resolve: got %q want s3cr3t", got)
	}
}

func TestKeychainBackend_ResolveErrors(t *testing.T) {
	tests := []struct {
		name string
		ref  Ref
		err  error
		kind ErrorKind
	}{
		{
			name: "invalid path",
			ref:  Ref{Raw: "keychain://cloudstic", Scheme: "keychain", Path: "cloudstic"},
			kind: KindInvalidRef,
		},
		{
			name: "not found",
			ref:  Ref{Raw: "keychain://cloudstic/prod/password", Scheme: "keychain", Path: "cloudstic/prod/password"},
			err:  errKeychainNotFound,
			kind: KindNotFound,
		},
		{
			name: "unavailable",
			ref:  Ref{Raw: "keychain://cloudstic/prod/password", Scheme: "keychain", Path: "cloudstic/prod/password"},
			err:  errKeychainUnavailable,
			kind: KindBackendUnavailable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newKeychainBackendWithLookup(func(context.Context, string, string) (string, error) {
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
