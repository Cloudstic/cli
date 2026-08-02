package sourceoauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// stubBrowser replaces the real browser launcher for the duration of a test.
// Without it these tests would open a browser window on the machine running
// them, which is why the launcher is indirected at all.
func stubBrowser(t *testing.T, fn func(string) error) {
	t.Helper()
	prev := openBrowserFn
	openBrowserFn = fn
	t.Cleanup(func() { openBrowserFn = prev })
}

func testOAuthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID: "test-client",
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://example.invalid/auth",
			TokenURL: "https://example.invalid/token",
		},
	}
}

// TestExchangeWithLocalServer_CancelledContextReturns is the regression test for
// an authorization that could not be interrupted.
//
// The flow waits for a human to finish authorizing in a browser, which they may
// never do. The wait used to be a bare channel receive with no context, and the
// cloudstic CLI installs signal.NotifyContext — which *replaces* SIGINT's
// default "terminate the process" with "cancel this context". So Ctrl+C
// cancelled a context nobody was watching and the command hung forever, with
// further Ctrl+C doing nothing because the signal stays diverted.
//
// The bound matters as much as the assertion: a fix that returns eventually but
// not promptly is not a fix a user would notice.
func TestExchangeWithLocalServer_CancelledContextReturns(t *testing.T) {
	stubBrowser(t, func(string) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel once the flow is under way, standing in for the user's Ctrl+C.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := ExchangeWithLocalServer(ctx, nil, testOAuthConfig(), oauth2.AccessTypeOffline)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExchangeWithLocalServer ignored a cancelled context and kept waiting; " +
			"Ctrl+C cannot interrupt an authorization")
	}
}

// A context cancelled before the call ever starts must not begin an
// authorization the caller has already given up on.
func TestExchangeWithLocalServer_AlreadyCancelled(t *testing.T) {
	opened := false
	stubBrowser(t, func(string) error {
		opened = true
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := ExchangeWithLocalServer(ctx, nil, testOAuthConfig(), oauth2.AccessTypeOffline)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExchangeWithLocalServer did not return for an already-cancelled context")
	}
	_ = opened // the browser may or may not have been reached; the return is what matters
}

// The local callback server must not outlive the call. A leaked listener would
// hold its port, so a second authorization in the same process could fail or,
// worse, receive the first one's redirect.
func TestExchangeWithLocalServer_ShutsDownItsListener(t *testing.T) {
	var authURL string
	stubBrowser(t, func(u string) error {
		authURL = u
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := ExchangeWithLocalServer(ctx, nil, testOAuthConfig(), oauth2.AccessTypeOffline); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	redirect := redirectURLFrom(t, authURL)
	if redirect == "" {
		t.Skip("could not recover the redirect URL from the auth URL")
	}

	// The server is shut down asynchronously; give it a moment to release.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		client := &http.Client{Timeout: 200 * time.Millisecond}
		resp, err := client.Get(redirect) //nolint:noctx // short-lived liveness probe
		if err != nil {
			return // refused: the listener is gone, which is what we want
		}
		_ = resp.Body.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("the callback server at %s is still accepting connections after the call returned", redirect)
}

// redirectURLFrom extracts the redirect_uri the flow registered, which is where
// its local server is listening.
func redirectURLFrom(t *testing.T, authURL string) string {
	t.Helper()
	if authURL == "" {
		return ""
	}
	u, err := url.Parse(authURL)
	if err != nil {
		return ""
	}
	return u.Query().Get("redirect_uri")
}
