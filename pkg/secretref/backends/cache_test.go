package backends

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudstic/cli/pkg/secretref"
)

// countingBackend counts Resolve calls and can gate them so concurrent callers
// pile up, exercising singleflight.
type countingBackend struct {
	value   string
	calls   atomic.Int64
	release chan struct{} // if non-nil, Resolve blocks until closed
	entered chan struct{} // if non-nil, signaled once per Resolve entry
}

func (b *countingBackend) Resolve(_ context.Context, _ secretref.Ref) (string, error) {
	b.calls.Add(1)
	if b.entered != nil {
		b.entered <- struct{}{}
	}
	if b.release != nil {
		<-b.release
	}
	return b.value, nil
}

func TestResolver_CachesNativeScheme(t *testing.T) {
	b := &countingBackend{value: "sekret"}
	r := secretref.NewResolver(map[string]secretref.Backend{"keychain": b})

	for range 3 {
		got, err := r.Resolve(context.Background(), "keychain://cloudstic/store/prod/password")
		if err != nil || got != "sekret" {
			t.Fatalf("resolve = %q, %v", got, err)
		}
	}
	if n := b.calls.Load(); n != 1 {
		t.Fatalf("keychain backend called %d times; want 1 (cached)", n)
	}
}

func TestResolver_DoesNotCacheEnv(t *testing.T) {
	values := map[string]string{"CLOUDSTIC_PASSWORD": "a"}
	var lookups atomic.Int64
	r := secretref.NewResolver(map[string]secretref.Backend{"env": NewEnvBackend(func(name string) (string, bool) {
		lookups.Add(1)
		v, ok := values[name]
		return v, ok
	})})

	for range 3 {
		if _, err := r.Resolve(context.Background(), "env://CLOUDSTIC_PASSWORD"); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if n := lookups.Load(); n != 3 {
		t.Fatalf("env lookups=%d; want 3 (env must not be cached)", n)
	}
}

func TestResolver_ConcurrentResolveDeduplicates(t *testing.T) {
	const n = 24
	b := &countingBackend{
		value:   "sekret",
		release: make(chan struct{}),
		entered: make(chan struct{}, 1),
	}
	r := secretref.NewResolver(map[string]secretref.Backend{"keychain": b})

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			v, err := r.Resolve(context.Background(), "keychain://cloudstic/store/prod/password")
			if err == nil && v != "sekret" {
				err = errNonMatch
			}
			errs <- err
		})
	}

	// Wait until the single in-flight backend call has started, then release it
	// so all waiters share its result.
	<-b.entered
	close(b.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent resolve error: %v", err)
		}
	}
	if got := b.calls.Load(); got != 1 {
		t.Fatalf("backend called %d times under concurrency; want 1 (singleflight)", got)
	}
}

func TestResolver_StoreWriteThroughAvoidsResolve(t *testing.T) {
	b := &writeThroughBackend{stored: map[string]string{}}
	r := secretref.NewResolver(map[string]secretref.Backend{"keychain": b})

	ref := "keychain://cloudstic/store/prod/password"
	if err := r.Store(context.Background(), ref, "written"); err != nil {
		t.Fatalf("store: %v", err)
	}
	got, err := r.Resolve(context.Background(), ref)
	if err != nil || got != "written" {
		t.Fatalf("resolve after store = %q, %v; want written", got, err)
	}
	if n := b.calls.Load(); n != 0 {
		t.Fatalf("backend Resolve called %d times; want 0 (served from write-through cache)", n)
	}
}

type writeThroughBackend struct {
	countingBackend
	mu     sync.Mutex
	stored map[string]string
}

func (b *writeThroughBackend) Scheme() string       { return "keychain" }
func (b *writeThroughBackend) DisplayName() string  { return "Test Keychain" }
func (b *writeThroughBackend) WriteSupported() bool { return true }
func (b *writeThroughBackend) DefaultRef(string, string) string {
	return "keychain://cloudstic/store/prod/password"
}
func (b *writeThroughBackend) Exists(_ context.Context, ref secretref.Ref) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.stored[ref.Raw]
	return ok, nil
}
func (b *writeThroughBackend) Store(_ context.Context, ref secretref.Ref, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stored[ref.Raw] = value
	return nil
}

var errNonMatch = &secretref.Error{Kind: secretref.KindBackendUnavailable, Detail: "value mismatch"}
