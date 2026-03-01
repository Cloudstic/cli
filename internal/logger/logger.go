package logger

import (
	"fmt"
	"io"
)

// Writer is the global destination for debug logs.
// If nil, no debug output is produced.
var Writer io.Writer

const (
	ColorBold   = "\033[1m"
	ColorDim    = "\033[2m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorCyan   = "\033[36m"
	ColorReset  = "\033[0m"
)

// IsDebug returns true if debug logging is enabled.
func IsDebug() bool {
	return Writer != nil
}

// Debugf formats and writes a message to the debug Writer if it is non-nil.
func Debugf(format string, args ...any) {
	if Writer != nil {
		_, _ = fmt.Fprintf(Writer, format+"\n", args...)
	}
}

// Logger allows component-specific prefixing for debug messages.
type Logger struct {
	prefix string
}

// New returns a Logger with the specified component name and color.
func New(component, color string) *Logger {
	prefix := ""
	if component != "" {
		if color != "" {
			prefix = color + "[" + component + "]" + ColorReset + " "
		} else {
			prefix = "[" + component + "] "
		}
	}
	return &Logger{prefix: prefix}
}

// Debugf formats and prints a debug message if the global logger.Writer is set.
func (l *Logger) Debugf(format string, args ...any) {
	if Writer != nil {
		_, _ = fmt.Fprintf(Writer, l.prefix+format+"\n", args...)
	}
}
