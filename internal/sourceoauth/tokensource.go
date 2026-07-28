package sourceoauth

import (
	"sync"

	"github.com/cloudstic/cli/internal/logger"
	"golang.org/x/oauth2"
)

// persistentTokenSource wraps an oauth2.TokenSource and calls a save function
// whenever a new token is produced (e.g. after a refresh).
type persistentTokenSource struct {
	ts      oauth2.TokenSource
	save    func(*oauth2.Token) error
	lastTok *oauth2.Token
	mu      sync.Mutex
	// log is the sink for the one diagnostic this type emits. A nil logger
	// logs nothing, which is what a caller that supplied no sink gets.
	log *logger.Logger
}

// NewPersistentTokenSource returns a TokenSource that delegates to ts and
// calls save whenever it yields an access token different from lastTok, so a
// refreshed token is written back to wherever it came from. A save error is
// logged and swallowed: the token is still valid for the current session.
//
// lastTok is the token already on disk; pass it so the first call does not
// re-save an unchanged token.
func NewPersistentTokenSource(ts oauth2.TokenSource, lastTok *oauth2.Token, save func(*oauth2.Token) error, log *logger.Logger) oauth2.TokenSource {
	return &persistentTokenSource{ts: ts, lastTok: lastTok, save: save, log: log}
}

func (pts *persistentTokenSource) Token() (*oauth2.Token, error) {
	tok, err := pts.ts.Token()
	if err != nil {
		return nil, err
	}
	pts.mu.Lock()
	shouldSave := pts.lastTok == nil || tok.AccessToken != pts.lastTok.AccessToken
	if shouldSave {
		if err := pts.save(tok); err != nil {
			// Log error but don't fail, as the token is still valid for this session.
			pts.log.Debugf("failed to persist refreshed OAuth token: %v", err)
		}
		pts.lastTok = tok
	}
	pts.mu.Unlock()
	return tok, nil
}
