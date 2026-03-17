package secretref

import (
	"context"
	"errors"
	"testing"
)

func TestParseWincredTarget(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{name: "basic", path: "cloudstic/store/prod/password", want: "cloudstic/store/prod/password"},
		{name: "leading slash", path: "/cloudstic/store/prod/password", want: "cloudstic/store/prod/password"},
		{name: "trim spaces", path: "  cloudstic/store/prod/password  ", want: "cloudstic/store/prod/password"},
		{name: "empty", path: "", wantErr: true},
		{name: "slash only", path: "/", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWincredTarget(tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseWincredTarget(%q): expected error", tc.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWincredTarget(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("parseWincredTarget(%q): got %q want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestWincredBackendResolve(t *testing.T) {
	b := newWincredBackendWithLookup(func(_ context.Context, target string) (string, error) {
		if target != "cloudstic/store/prod/password" {
			t.Fatalf("unexpected lookup target %q", target)
		}
		return "s3cr3t", nil
	})

	got, err := b.Resolve(context.Background(), Ref{Raw: "wincred://cloudstic/store/prod/password", Scheme: "wincred", Path: "cloudstic/store/prod/password"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "s3cr3t" {
		t.Fatalf("Resolve: got %q want s3cr3t", got)
	}
}

func TestWincredBackendResolveErrors(t *testing.T) {
	tests := []struct {
		name string
		ref  Ref
		err  error
		kind ErrorKind
	}{
		{
			name: "invalid path",
			ref:  Ref{Raw: "wincred://", Scheme: "wincred", Path: ""},
			kind: KindInvalidRef,
		},
		{
			name: "not found",
			ref:  Ref{Raw: "wincred://cloudstic/store/prod/password", Scheme: "wincred", Path: "cloudstic/store/prod/password"},
			err:  errWincredNotFound,
			kind: KindNotFound,
		},
		{
			name: "unavailable",
			ref:  Ref{Raw: "wincred://cloudstic/store/prod/password", Scheme: "wincred", Path: "cloudstic/store/prod/password"},
			err:  errWincredUnavailable,
			kind: KindBackendUnavailable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newWincredBackendWithLookup(func(context.Context, string) (string, error) {
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
