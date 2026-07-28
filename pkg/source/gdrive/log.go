package gdrive

import (
	"io"

	"github.com/cloudstic/cli/internal/logger"
)

// oauthLogger returns the sink for this source's OAuth diagnostics, or nil
// when the caller supplied none — in which case nothing is logged.
func oauthLogger(w io.Writer) *logger.Logger {
	if w == nil {
		return nil
	}
	return logger.New("gdrive", logger.ColorCyan).To(w)
}
