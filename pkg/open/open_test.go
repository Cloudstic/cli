package open

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/store"
	localstore "github.com/cloudstic/cli/pkg/store/local"
)

// Store construction takes a config value, so it is exercised without going
// through any flag parsing at all — which is the point of the pkg/config and
// pkg/open split.

func TestStore_LocalRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Store(ctx, config.Store{URI: "local:" + t.TempDir()})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok := s.(*localstore.Store); !ok {
		t.Fatalf("expected *localstore.Store, got %T", s)
	}

	if err := s.Put(ctx, "config", []byte("marker")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(ctx, "config")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "marker" {
		t.Errorf("Get = %q, want %q", got, "marker")
	}
}

func TestStore_UnknownScheme(t *testing.T) {
	if _, err := Store(context.Background(), config.Store{URI: "nope:whatever"}); err == nil {
		t.Fatal("expected an error for an unknown store scheme")
	}
}

// TestStore_B2RequiresCredentials checks the credential guard that lives here
// rather than in the B2 backend, so a missing credential is caught before any
// network dial is attempted. Its message names both the flags and the
// environment variables that satisfy it.
func TestStore_B2RequiresCredentials(t *testing.T) {
	cases := []config.Store{
		{URI: "b2:my-bucket"},
		{URI: "b2:my-bucket", B2: config.B2{KeyID: "id-only"}},
		{URI: "b2:my-bucket", B2: config.B2{AppKey: "key-only"}},
	}
	for _, cfg := range cases {
		_, err := Store(context.Background(), cfg)
		if err == nil {
			t.Fatalf("expected an error for incomplete B2 credentials: %+v", cfg.B2)
			continue
		}
		for _, want := range []string{"-b2-key-id", "B2_KEY_ID"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to mention %q", err, want)
			}
		}
	}
}

func TestStore_WithDebugWriterWraps(t *testing.T) {
	var buf bytes.Buffer

	s, err := Store(context.Background(), config.Store{URI: "local:" + t.TempDir()},
		WithDebugWriter(&buf))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok := s.(*store.DebugStore); !ok {
		t.Fatalf("expected the store to be wrapped in *store.DebugStore, got %T", s)
	}

	if err := s.Put(context.Background(), "config", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("the debug writer received nothing; the wrapper is not logging through it")
	}
}

func TestStore_WithoutDebugWriterIsUnwrapped(t *testing.T) {
	s, err := Store(context.Background(), config.Store{URI: "local:" + t.TempDir()})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok := s.(*store.DebugStore); ok {
		t.Error("no debug writer was supplied, so the store must not be wrapped")
	}
}

// countingStore records that it was interposed, so the test can tell where in
// the chain a backend wrapper ended up.
type countingStore struct {
	store.ObjectStore
	puts *int
}

func (c countingStore) Put(ctx context.Context, key string, data []byte) error {
	*c.puts++
	return c.ObjectStore.Put(ctx, key, data)
}

// TestStore_BackendWrapperSitsBelowDebug pins the order the two decorators are
// applied in. A wrapper is meant to see what actually reaches the backend, so
// it belongs underneath the debug layer rather than on top of it — which is
// also the order the CLI's crash-injection hook has always had.
func TestStore_BackendWrapperSitsBelowDebug(t *testing.T) {
	var buf bytes.Buffer
	puts := 0

	s, err := Store(context.Background(), config.Store{URI: "local:" + t.TempDir()},
		WithBackendWrapper(func(inner store.ObjectStore) (store.ObjectStore, error) {
			return countingStore{ObjectStore: inner, puts: &puts}, nil
		}),
		WithDebugWriter(&buf),
	)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok := s.(*store.DebugStore); !ok {
		t.Fatalf("the outermost layer must be the debug store, got %T", s)
	}
	if err := s.Put(context.Background(), "config", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if puts != 1 {
		t.Errorf("wrapper saw %d puts, want 1: it must be interposed, not bypassed", puts)
	}
}

func TestStore_BackendWrapperErrorPropagates(t *testing.T) {
	_, err := Store(context.Background(), config.Store{URI: "local:" + t.TempDir()},
		WithBackendWrapper(func(store.ObjectStore) (store.ObjectStore, error) {
			return nil, errWrapper
		}))
	if err == nil {
		t.Fatal("expected the wrapper's error to propagate")
	}
}

type wrapperError struct{}

func (wrapperError) Error() string { return "wrapper failed" }

var errWrapper = wrapperError{}

func TestS3Region(t *testing.T) {
	if got := s3Region(""); got != defaultS3Region {
		t.Errorf("s3Region(%q) = %q, want the built-in default %q", "", got, defaultS3Region)
	}
	if got := s3Region("eu-west-3"); got != "eu-west-3" {
		t.Errorf("s3Region(%q) = %q, want it left alone", "eu-west-3", got)
	}
}
