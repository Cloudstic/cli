package storelayer

import "github.com/cloudstic/cli/internal/logger"

// The label stays "store" rather than "storelayer": these layers are the store
// as far as a user reading debug output is concerned, and renaming it would
// change observable debug output for no reason. pkg/store no longer needs a
// logger of its own — DebugStore writes to the io.Writer it is given.
var debugLog = logger.New("store", logger.ColorYellow)

func debugf(format string, args ...any) {
	debugLog.Debugf(format, args...)
}
