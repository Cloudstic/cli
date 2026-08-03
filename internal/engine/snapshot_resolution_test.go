package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

type listRejectingStore struct {
	store.ObjectStore
}

func (s *listRejectingStore) List(context.Context, string) ([]string, error) {
	panic("List called while resolving a full snapshot hash")
}

func TestSnapshotReadersDoNotListForFullHash(t *testing.T) {
	ctx := context.Background()
	base := NewMockStore()
	ref := putSnapshot(t, base, &core.Snapshot{Seq: 1})
	s := &listRejectingStore{ObjectStore: base}

	tests := []struct {
		name    string
		resolve func(string) error
	}{
		{
			name: "restore",
			resolve: func(selector string) error {
				_, _, err := NewRestoreManager(Deps{Store: s, Reporter: ui.NewNoOpReporter()}).resolveSnapshot(ctx, selector)
				return err
			},
		},
		{
			name: "ls",
			resolve: func(selector string) error {
				_, _, err := NewLsSnapshotManager(Deps{Store: s}).resolveSnapshot(ctx, selector)
				return err
			},
		},
		{
			name: "diff",
			resolve: func(selector string) error {
				_, err := NewDiffManager(Deps{Store: s}).resolveSnapshot(ctx, selector)
				return err
			},
		},
	}

	selectors := []struct {
		name  string
		value string
	}{
		{name: "qualified", value: ref},
		{name: "bare", value: strings.TrimPrefix(ref, "snapshot/")},
	}
	for _, tt := range tests {
		for _, selector := range selectors {
			t.Run(tt.name+"/"+selector.name, func(t *testing.T) {
				if err := tt.resolve(selector.value); err != nil {
					t.Fatalf("resolve full hash: %v", err)
				}
			})
		}
	}
}

func TestSnapshotReadersResolveUniqueHashPrefix(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()
	ref := putSnapshot(t, s, &core.Snapshot{Seq: 1})
	prefix := strings.TrimPrefix(ref, "snapshot/")[:8]

	tests := []struct {
		name    string
		resolve func(string) (string, error)
	}{
		{
			name: "restore",
			resolve: func(selector string) (string, error) {
				_, resolved, err := NewRestoreManager(Deps{Store: s, Reporter: ui.NewNoOpReporter()}).resolveSnapshot(ctx, selector)
				return resolved, err
			},
		},
		{
			name: "ls",
			resolve: func(selector string) (string, error) {
				_, resolved, err := NewLsSnapshotManager(Deps{Store: s}).resolveSnapshot(ctx, selector)
				return resolved, err
			},
		},
		{
			name: "diff",
			resolve: func(selector string) (string, error) {
				return NewDiffManager(Deps{Store: s}).resolveSnapshot(ctx, selector)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.resolve(prefix)
			if err != nil {
				t.Fatalf("resolve %q: %v", prefix, err)
			}
			if got != ref {
				t.Fatalf("resolved ref = %q, want %q", got, ref)
			}
		})
	}
}

func TestSnapshotReadersRejectAmbiguousHashPrefix(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()
	const (
		prefix = "abc12345"
		ref1   = "snapshot/abc1234511111111111111111111111111111111111111111111111111111111"
		ref2   = "snapshot/abc1234522222222222222222222222222222222222222222222222222222222"
	)
	if err := s.Put(ctx, ref1, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, ref2, []byte("second")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		resolve func(string) error
	}{
		{
			name: "restore",
			resolve: func(selector string) error {
				_, _, err := NewRestoreManager(Deps{Store: s, Reporter: ui.NewNoOpReporter()}).resolveSnapshot(ctx, selector)
				return err
			},
		},
		{
			name: "ls",
			resolve: func(selector string) error {
				_, _, err := NewLsSnapshotManager(Deps{Store: s}).resolveSnapshot(ctx, selector)
				return err
			},
		},
		{
			name: "diff",
			resolve: func(selector string) error {
				_, err := NewDiffManager(Deps{Store: s}).resolveSnapshot(ctx, selector)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.resolve(prefix)
			if err == nil {
				t.Fatalf("resolve %q succeeded, want ambiguity error", prefix)
			}
			if !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("resolve %q error = %q, want ambiguity error", prefix, err)
			}
			if !errors.Is(err, ErrSnapshotRefAmbiguous) {
				t.Fatalf("resolve %q error = %q, want errors.Is(err, ErrSnapshotRefAmbiguous)", prefix, err)
			}
		})
	}
}

func TestSnapshotReadersReturnNotFoundSentinel(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	tests := []struct {
		name    string
		resolve func(string) error
	}{
		{
			name: "restore",
			resolve: func(selector string) error {
				_, _, err := NewRestoreManager(Deps{Store: s, Reporter: ui.NewNoOpReporter()}).resolveSnapshot(ctx, selector)
				return err
			},
		},
		{
			name: "ls",
			resolve: func(selector string) error {
				_, _, err := NewLsSnapshotManager(Deps{Store: s}).resolveSnapshot(ctx, selector)
				return err
			},
		},
		{
			name: "diff",
			resolve: func(selector string) error {
				_, _, err := NewDiffManager(Deps{Store: s}).loadRoot(ctx, selector)
				return err
			},
		},
	}
	selectors := []struct {
		name  string
		value string
	}{
		{name: "short-prefix", value: "deadbeef"},
		{name: "full-hash", value: strings.Repeat("f", sha256.Size*2)},
		{name: "latest", value: "latest"},
	}

	for _, tt := range tests {
		for _, selector := range selectors {
			t.Run(tt.name+"/"+selector.name, func(t *testing.T) {
				err := tt.resolve(selector.value)
				if err == nil {
					t.Fatalf("resolve %q succeeded, want not-found error", selector.value)
				}
				if !errors.Is(err, ErrSnapshotNotFound) {
					t.Fatalf("resolve %q error = %q, want errors.Is(err, ErrSnapshotNotFound)", selector.value, err)
				}
			})
		}
	}
}
