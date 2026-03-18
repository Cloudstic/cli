package source

import (
	"github.com/cloudstic/cli/internal/logger"
	"golang.org/x/oauth2"
)

// persistentTokenSource wraps an oauth2.TokenSource and calls a save function
// whenever a new token is produced (e.g. after a refresh).
type persistentTokenSource struct {
	ts   oauth2.TokenSource
	save func(*oauth2.Token) error
}

func (pts *persistentTokenSource) Token() (*oauth2.Token, error) {
	tok, err := pts.ts.Token()
	if err != nil {
		return nil, err
	}
	// Note: ts.Token() already handles reuse and refresh.
	// oauth2.ReuseTokenSource only returns a new token if it was refreshed.
	if err := pts.save(tok); err != nil {
		// Log error but don't fail, as the token is still valid for this session.
		logger.Debugf("failed to persist refreshed OAuth token: %v", err)
	}
	return tok, nil
}
