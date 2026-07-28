package sourceoauth

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type mockTokenSource struct {
	tok *oauth2.Token
	err error
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	return m.tok, m.err
}

func TestPersistentTokenSource(t *testing.T) {
	t1 := &oauth2.Token{AccessToken: "token1", Expiry: time.Now().Add(time.Hour)}
	t2 := &oauth2.Token{AccessToken: "token2", Expiry: time.Now().Add(2 * time.Hour)}

	mock := &mockTokenSource{tok: t1}
	saveCount := 0
	var lastSaved *oauth2.Token

	pts := &persistentTokenSource{
		ts:      mock,
		lastTok: t1,
		save: func(tok *oauth2.Token) error {
			saveCount++
			lastSaved = tok
			return nil
		},
	}

	// 1. Same token should not trigger save
	got, err := pts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "token1" {
		t.Errorf("got %q, want %q", got.AccessToken, "token1")
	}
	if saveCount != 0 {
		t.Errorf("expected 0 saves, got %d", saveCount)
	}

	// 2. Different token should trigger save
	mock.tok = t2
	got, err = pts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "token2" {
		t.Errorf("got %q, want %q", got.AccessToken, "token2")
	}
	if saveCount != 1 {
		t.Errorf("expected 1 save, got %d", saveCount)
	}
	if lastSaved.AccessToken != "token2" {
		t.Errorf("saved %q, want %q", lastSaved.AccessToken, "token2")
	}

	// 3. Error from underlying source
	mock.err = errors.New("fail")
	_, err = pts.Token()
	if err == nil {
		t.Error("expected error")
	}

	// 4. Save error should not prevent returning token
	mock.err = nil
	mock.tok = &oauth2.Token{AccessToken: "token3"}
	pts.save = func(tok *oauth2.Token) error {
		return errors.New("save fail")
	}
	got, err = pts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "token3" {
		t.Errorf("got %q, want %q", got.AccessToken, "token3")
	}
}

func TestPersistentTokenSource_ConcurrentCallsSaveOnce(t *testing.T) {
	tok := &oauth2.Token{AccessToken: "token1", Expiry: time.Now().Add(time.Hour)}
	mock := &mockTokenSource{tok: tok}
	var saves atomic.Int32
	pts := &persistentTokenSource{
		ts: mock,
		save: func(tok *oauth2.Token) error {
			saves.Add(1)
			time.Sleep(10 * time.Millisecond)
			return nil
		},
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := pts.Token()
			if err != nil {
				t.Errorf("Token failed: %v", err)
				return
			}
			if got.AccessToken != "token1" {
				t.Errorf("got %q want %q", got.AccessToken, "token1")
			}
		}()
	}
	wg.Wait()

	if got := saves.Load(); got != 1 {
		t.Fatalf("expected 1 save, got %d", got)
	}
}

// TestNewPersistentTokenSource covers the exported constructor, which is what
// the gdrive and onedrive sources actually call. The struct-literal tests above
// exercise the same behaviour but bypass the constructor's field wiring — a
// swapped lastTok/save argument would slip past them.
func TestNewPersistentTokenSource(t *testing.T) {
	initial := &oauth2.Token{AccessToken: "initial", Expiry: time.Now().Add(time.Hour)}
	refreshed := &oauth2.Token{AccessToken: "refreshed", Expiry: time.Now().Add(2 * time.Hour)}

	mock := &mockTokenSource{tok: initial}
	var saved []string
	ts := NewPersistentTokenSource(mock, initial, func(tok *oauth2.Token) error {
		saved = append(saved, tok.AccessToken)
		return nil
	})

	// lastTok matches, so the token already on disk is not rewritten.
	if _, err := ts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if len(saved) != 0 {
		t.Fatalf("expected no save for the unchanged token, got %v", saved)
	}

	// A refresh must be persisted.
	mock.tok = refreshed
	got, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got.AccessToken != "refreshed" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "refreshed")
	}
	if len(saved) != 1 || saved[0] != "refreshed" {
		t.Errorf("saved = %v, want [refreshed]", saved)
	}
}

// TestNewPersistentTokenSource_SaveErrorIsNotFatal documents the deliberate
// choice to swallow save failures: the token is still valid for this session,
// so a read-only token file must not break the backup in progress.
func TestNewPersistentTokenSource_SaveErrorIsNotFatal(t *testing.T) {
	tok := &oauth2.Token{AccessToken: "fresh", Expiry: time.Now().Add(time.Hour)}
	ts := NewPersistentTokenSource(&mockTokenSource{tok: tok}, nil, func(*oauth2.Token) error {
		return errors.New("disk full")
	})

	got, err := ts.Token()
	if err != nil {
		t.Fatalf("a save failure must not fail Token(): %v", err)
	}
	if got.AccessToken != "fresh" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "fresh")
	}
}
