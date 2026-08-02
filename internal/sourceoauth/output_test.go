package sourceoauth

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// TestExchangeWithLocalServer_WritesToTheGivenWriter is the regression test for
// a library writing to the process's stdout.
//
// The flow has two lines it must show a human — that a browser is opening, and
// the URL to visit if it did not. They went to os.Stdout via fmt.Printf, which a
// library has no claim on: a caller could neither capture nor redirect them, and
// `cloudstic auth login -json` had them land in the middle of its JSON stream.
func TestExchangeWithLocalServer_WritesToTheGivenWriter(t *testing.T) {
	stubBrowser(t, func(string) error { return nil })

	var out bytes.Buffer
	stdout := captureStdout(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		_, _ = ExchangeWithLocalServer(ctx, &out, testOAuthConfig(), oauth2.AccessTypeOffline)
	})

	if !strings.Contains(out.String(), "Opening browser") {
		t.Errorf("the writer received %q, want the browser banner", out.String())
	}
	if stdout != "" {
		t.Errorf("the flow wrote %q to os.Stdout; a library must write only where "+
			"the caller told it to, or -json output is corrupted", stdout)
	}
}

// A caller with nowhere to write must not have the lines fall back to stdout.
func TestExchangeWithLocalServer_NilWriterDiscards(t *testing.T) {
	stubBrowser(t, func(string) error { return nil })

	stdout := captureStdout(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		_, _ = ExchangeWithLocalServer(ctx, nil, testOAuthConfig(), oauth2.AccessTypeOffline)
	})
	if stdout != "" {
		t.Errorf("a nil writer leaked %q to os.Stdout", stdout)
	}
}

// When the browser cannot be opened, the URL is the only way for the user to
// proceed — so it must reach the writer, not vanish.
func TestExchangeWithLocalServer_UnopenableBrowserPrintsTheURL(t *testing.T) {
	stubBrowser(t, func(string) error { return os.ErrNotExist })

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, _ = ExchangeWithLocalServer(ctx, &out, testOAuthConfig(), oauth2.AccessTypeOffline)

	got := out.String()
	if !strings.Contains(got, "Could not open browser") || !strings.Contains(got, "https://example.invalid/auth") {
		t.Errorf("writer received %q, want the fallback message and the auth URL", got)
	}
}

// captureStdout runs fn while capturing os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}
