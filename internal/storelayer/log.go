package storelayer

import (
	"io"

	"github.com/cloudstic/cli/internal/logger"
)

// The label stays "store" rather than "storelayer": these layers are the store
// as far as a user reading debug output is concerned, and renaming it would
// change observable debug output for no reason. pkg/store no longer needs a
// logger of its own — DebugStore writes to the io.Writer it is given.
var debugLog = logger.New("store", logger.ColorYellow)

// debugf writes through the sink this store was given, falling back to the
// process-wide one when it was given none — which is what every caller got
// before sinks were injectable (RFC 0022 §8).
//
// A nil logger is tolerated rather than guarded at every call site, so a
// PackStore built in a test without options still logs somewhere valid.
func (s *PackStore) debugf(format string, args ...any) {
	if s.log == nil {
		debugLog.Debugf(format, args...)
		return
	}
	s.log.Debugf(format, args...)
}

// WithPackLogger sends this store's debug output to w.
func WithPackLogger(w io.Writer) PackOption {
	return func(s *PackStore) { s.log = debugLog.To(w) }
}
