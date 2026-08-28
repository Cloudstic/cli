package s3

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/cloudstic/cli/pkg/store"
)

// The partial-failure and batch-splitting behaviour cannot be provoked from a
// real server: MinIO will not refuse one key of a batch on request, and asserting
// the 1,000-key split would mean putting 1,001 objects into a container. Both
// are properties of how this package builds requests and reads responses, so
// they are exercised against a stub endpoint that answers DeleteObjects.

// deleteObjectsRequest is the request body S3 receives.
type deleteObjectsRequest struct {
	Objects []struct {
		Key string `xml:"Key"`
	} `xml:"Object"`
}

// stubDeleteServer answers DeleteObjects, recording the batches it was sent and
// letting a test decide each key's verdict.
type stubDeleteServer struct {
	mu      sync.Mutex
	batches [][]string

	// verdict returns the per-key outcome: an empty code means deleted, and
	// "" with omit=true means the key is left out of the response entirely.
	verdict func(key string) (code string, omit bool)
}

func (d *stubDeleteServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req deleteObjectsRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	keys := make([]string, len(req.Objects))
	for i, o := range req.Objects {
		keys[i] = o.Key
	}
	d.mu.Lock()
	d.batches = append(d.batches, keys)
	d.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	for _, key := range keys {
		code, omit := "", false
		if d.verdict != nil {
			code, omit = d.verdict(key)
		}
		switch {
		case omit:
		case code == "":
			fmt.Fprintf(&b, "<Deleted><Key>%s</Key></Deleted>", key)
		default:
			fmt.Fprintf(&b, "<Error><Key>%s</Key><Code>%s</Code><Message>refused</Message></Error>", key, code)
		}
	}
	b.WriteString(`</DeleteResult>`)

	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, b.String())
}

func newStubStore(t *testing.T, d *stubDeleteServer, opts ...Option) *Store {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)

	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("key", "secret", "")),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.UsePathStyle = true
	})

	st, err := New(context.Background(), "bucket", append([]Option{WithS3Client(client)}, opts...)...)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return st
}

// DeleteObjects takes at most 1,000 keys, and exceeding it is a request S3
// rejects outright rather than truncates.
func TestDeleteAll_SplitsAtTheThousandKeyLimit(t *testing.T) {
	stub := &stubDeleteServer{}
	st := newStubStore(t, stub)

	keys := make([]string, 2500)
	for i := range keys {
		keys[i] = fmt.Sprintf("chunk/%04d", i)
	}
	if err := st.DeleteAll(context.Background(), keys); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}

	var sizes []int
	var seen []string
	for _, b := range stub.batches {
		sizes = append(sizes, len(b))
		seen = append(seen, b...)
	}
	if len(sizes) != 3 || sizes[0] != 1000 || sizes[1] != 1000 || sizes[2] != 500 {
		t.Errorf("batch sizes = %v, want [1000 1000 500]", sizes)
	}
	if len(seen) != len(keys) {
		t.Fatalf("sent %d keys, asked for %d", len(seen), len(keys))
	}
	for i, key := range keys {
		if seen[i] != key {
			t.Fatalf("key %d = %q, want %q", i, seen[i], key)
			break
		}
	}
}

// The response carries a verdict per key, and a sweep that read a partial
// failure as "all deleted" would report space it did not reclaim.
func TestDeleteAll_ReportsRefusedKeysIndividually(t *testing.T) {
	stub := &stubDeleteServer{
		verdict: func(key string) (string, bool) {
			if key == "chunk/b" {
				return "AccessDenied", false
			}
			return "", false
		},
	}
	st := newStubStore(t, stub)

	err := st.DeleteAll(context.Background(), []string{"chunk/a", "chunk/b", "chunk/c"})
	if err == nil {
		t.Fatal("a refused key must not be reported as success")
	}

	failed, ok := store.FailedDeletes(err)
	if !ok {
		t.Fatalf("failures must be reported per key, got %v", err)
	}
	if got := failed.Keys(); len(got) != 1 || got[0] != "chunk/b" {
		t.Errorf("failed keys = %v, want [chunk/b]", got)
	}
	if !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("the backend's reason should survive, got %v", err)
	}
}

// S3 promises a result per key. A key mentioned neither way is unaccounted for,
// which must round to "still there" rather than "deleted" — an S3-compatible
// service is not AWS, and this is the shape a truncated response takes.
func TestDeleteAll_TreatsAnUnmentionedKeyAsUnconfirmed(t *testing.T) {
	stub := &stubDeleteServer{
		verdict: func(key string) (string, bool) {
			return "", key == "chunk/b"
		},
	}
	st := newStubStore(t, stub)

	err := st.DeleteAll(context.Background(), []string{"chunk/a", "chunk/b"})
	failed, ok := store.FailedDeletes(err)
	if !ok {
		t.Fatalf("expected per-key failures, got %v", err)
	}
	if got := failed.Keys(); len(got) != 1 || got[0] != "chunk/b" {
		t.Errorf("failed keys = %v, want [chunk/b]", got)
	}
}

// Keys are prefixed on the way out and must be reported back unprefixed, or a
// caller could not match a failure to the key it asked about.
func TestDeleteAll_ReportsFailuresWithoutTheStorePrefix(t *testing.T) {
	stub := &stubDeleteServer{
		verdict: func(string) (string, bool) { return "AccessDenied", false },
	}
	st := newStubStore(t, stub, WithPrefix("repo"))

	err := st.DeleteAll(context.Background(), []string{"chunk/a"})
	failed, ok := store.FailedDeletes(err)
	if !ok {
		t.Fatalf("expected per-key failures, got %v", err)
	}
	if got := failed.Keys(); len(got) != 1 || got[0] != "chunk/a" {
		t.Errorf("failed keys = %v, want [chunk/a]", got)
	}
	if sent := stub.batches[0][0]; sent != "repo/chunk/a" {
		t.Errorf("sent key = %q, want the prefixed form", sent)
	}
}

// A request that never landed leaves its own keys unconfirmed without
// invalidating the batches that did land: DeleteAll spans several requests, and
// collapsing them would lose deletions the caller may legitimately count.
func TestDeleteAll_KeepsEarlierBatchesCreditableWhenOneRequestFails(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req deleteObjectsRequest
		_ = xml.Unmarshal(body, &req)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><DeleteResult>`)
		for _, o := range req.Objects {
			fmt.Fprintf(&b, "<Deleted><Key>%s</Key></Deleted>", o.Key)
		}
		b.WriteString(`</DeleteResult>`)
		_, _ = io.WriteString(w, b.String())
	}))
	defer srv.Close()

	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("us-east-1"),
		config.WithRetryMaxAttempts(1),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("key", "secret", "")),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.UsePathStyle = true
	})
	st, err := New(context.Background(), "bucket", WithS3Client(client))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	keys := make([]string, 1500)
	for i := range keys {
		keys[i] = fmt.Sprintf("chunk/%04d", i)
	}

	failed, ok := store.FailedDeletes(st.DeleteAll(context.Background(), keys))
	if !ok {
		t.Fatal("expected per-key failures")
	}
	// Only the second request's 500 keys are unconfirmed; the first request's
	// 1,000 were confirmed deleted and stay creditable.
	if len(failed) != 500 {
		t.Errorf("unconfirmed keys = %d, want 500", len(failed))
	}
	for _, de := range failed {
		if !strings.HasPrefix(de.Key, "chunk/1") {
			t.Fatalf("unexpected unconfirmed key %q", de.Key)
			break
		}
	}
}
