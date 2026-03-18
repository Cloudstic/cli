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
	b := newKeychainBackendWithLookup(func(_ context.Context, service, account string) ([]byte, error) {
		if service != "cloudstic/prod" || account != "password" {
			t.Fatalf("unexpected lookup args: %q/%q", service, account)
		}
		return []byte("s3cr3t"), nil
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
			b := newKeychainBackendWithLookup(func(context.Context, string, string) ([]byte, error) {
				if tc.err == nil {
					return nil, nil
				}
				return nil, tc.err
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

func TestKeychainBackend_DefaultRef(t *testing.T) {
	b := NewKeychainBackend()
	if got := b.DefaultRef("prod", "password"); got != "keychain://cloudstic/store/prod/password" {
		t.Fatalf("DefaultRef() = %q", got)
	}
}

func TestKeychainBackend_Exists(t *testing.T) {
	b := newKeychainBackendWithFns(
		func(context.Context, string, string) ([]byte, error) { return nil, nil },
		func(_ context.Context, service, account string) (bool, error) {
			if service != "cloudstic/store/prod" || account != "password" {
				t.Fatalf("unexpected args %q/%q", service, account)
			}
			return true, nil
		},
		nil,
		nil,
	)
	exists, err := b.Exists(context.Background(), Ref{Raw: "keychain://cloudstic/store/prod/password", Scheme: "keychain", Path: "cloudstic/store/prod/password"})
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
}

func TestKeychainBackend_Store(t *testing.T) {
	b := newKeychainBackendWithFns(
		func(context.Context, string, string) ([]byte, error) { return nil, nil },
		nil,
		func(_ context.Context, service, account string, value []byte) error {
			if service != "cloudstic/store/prod" || account != "password" || string(value) != "secret" {
				t.Fatalf("unexpected args %q/%q/%q", service, account, string(value))
			}
			return nil
		},
		nil,
	)
	if err := b.Store(context.Background(), Ref{Raw: "keychain://cloudstic/store/prod/password", Scheme: "keychain", Path: "cloudstic/store/prod/password"}, "secret"); err != nil {
		t.Fatalf("Store: %v", err)
	}
}

func TestKeychainBackend_BlobRoundTrip(t *testing.T) {
	var storedValue []byte
	b := newKeychainBackendWithFns(
		func(context.Context, string, string) ([]byte, error) { return storedValue, nil },
		nil,
		func(_ context.Context, service, account string, value []byte) error {
			storedValue = value
			return nil
		},
		func(_ context.Context, service, account string) error {
			storedValue = nil
			return nil
		},
	)

	ctx := context.Background()
	ref := Ref{Raw: "keychain://cloudstic/token/gdrive", Scheme: "keychain", Path: "cloudstic/token/gdrive"}
	data := []byte("binary-data")

	if err := b.SaveBlob(ctx, ref, data); err != nil {
		t.Fatalf("SaveBlob: %v", err)
	}
	got, err := b.LoadBlob(ctx, ref)
	if err != nil {
		t.Fatalf("LoadBlob: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q want %q", string(got), string(data))
	}
	if err := b.DeleteBlob(ctx, ref); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}
	if storedValue != nil {
		t.Fatal("expected storedValue to be nil after delete")
	}
}
