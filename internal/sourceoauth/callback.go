package sourceoauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
)

// ExchangeWithLocalServer performs the OAuth2 authorization code flow by
// spinning up a temporary local HTTP server to receive the callback. It
// opens the user's default browser to the consent page and automatically
// captures the authorization code, eliminating the need to copy-paste.
//
// If the browser cannot be opened, the auth URL is printed so the user can
// navigate to it manually; the local server still captures the redirect.
//
// ctx governs the wait. This is the whole reason it is a parameter: the flow
// blocks until a human finishes authorizing in a browser, which they may never
// do, and the cloudstic CLI installs a SIGINT handler that cancels a context
// rather than killing the process — so a wait that ignored ctx could not be
// interrupted with Ctrl+C at all, and pressing it again would not help either.
// Cancellation returns ctx.Err() promptly and shuts the local server down.
func ExchangeWithLocalServer(ctx context.Context, config *oauth2.Config, authCodeOpts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	verifier := oauth2.GenerateVerifier()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on localhost: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	origRedirect := config.RedirectURL
	config.RedirectURL = fmt.Sprintf("http://localhost:%d/callback", port)
	defer func() { config.RedirectURL = origRedirect }()

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "invalid state parameter", http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("state mismatch in OAuth callback")}
			return
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			desc := r.URL.Query().Get("error_description")
			http.Error(w, "Authorization failed: "+errMsg, http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("OAuth error: %s – %s", errMsg, desc)}
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:system-ui,sans-serif;display:flex;justify-content:center;align-items:center;height:100vh;margin:0"><div style="text-align:center"><h2>Authorization successful</h2><p>You can close this tab and return to the terminal.</p></div></body></html>`)
		ch <- result{code: r.URL.Query().Get("code")}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		// Not ctx: on cancellation ctx is already done, and passing it here
		// would skip the graceful shutdown entirely. A short bounded deadline
		// lets an in-flight callback response finish without letting a wedged
		// connection hold the process open.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// PKCE: send code_challenge with the auth request
	opts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}, authCodeOpts...)
	authURL := config.AuthCodeURL(state, opts...)

	fmt.Printf("Opening browser for authorization...\n")
	if err := openBrowserFn(authURL); err != nil {
		fmt.Printf("Could not open browser automatically.\nPlease visit this URL:\n%s\n", authURL)
	}

	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, res.err
	}

	tok, err := config.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange token: %w", err)
	}
	return tok, nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// openBrowserFn is the browser launcher, indirected so tests can exercise the
// flow without opening a real browser on the machine running them. Production
// code never reassigns it.
var openBrowserFn = openBrowser

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
	return cmd.Start()
}
