// Package logger provides the component-prefixed debug output that the client,
// engine, source, and store layers write when debugging is enabled.
//
// A Logger writes to the sink it was given and nowhere else. There is no
// package-level destination: every component takes a sink from whoever
// constructed it, so two clients in one process can log to different places,
// or one can log while the other stays silent. A nil *Logger, and a logger
// bound to a nil writer, both discard — which lets a caller with no sink to
// offer pass one through unconditionally instead of branching (RFC 0022 §8).
package logger

import (
	"fmt"
	"io"
)

const (
	ColorBold   = "\033[1m"
	ColorDim    = "\033[2m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorCyan   = "\033[36m"
	ColorReset  = "\033[0m"
)

// Logger allows component-specific prefixing for debug messages.
type Logger struct {
	prefix string
	w      io.Writer
}

// New returns a Logger with the specified component name and color, and no
// sink — it discards until one is bound with To.
//
// Use it for a package-level default that declares a component's prefix and
// colour once, then bind a sink with To at each construction site.
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

// To returns a copy of l that writes to w, keeping its prefix.
//
// A nil w yields a discarding logger, so a caller that has no sink to offer
// can pass one through unconditionally instead of branching.
func (l *Logger) To(w io.Writer) *Logger {
	if l == nil {
		return nil
	}
	c := *l
	c.w = w
	return &c
}

// Debugf formats and prints a debug message to this logger's sink, discarding
// it if the logger has none.
func (l *Logger) Debugf(format string, args ...any) {
	if l == nil {
		return
	}
	if l.w != nil {
		_, _ = fmt.Fprintf(l.w, l.prefix+format+"\n", args...)
	}
}
