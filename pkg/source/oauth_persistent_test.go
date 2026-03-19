package source

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
