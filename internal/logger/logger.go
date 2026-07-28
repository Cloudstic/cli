// Package logger provides the component-prefixed debug output that the client,
// engine, and store layers write when debugging is enabled.
//
// A Logger writes to the sink it was given. Loggers created by New have no
// sink of their own and fall back to the package-level Writer, which is how
// every call site behaved before sinks were injectable and how the call sites
// that have not been converted yet still behave (RFC 0022 §8).
//
// The distinction matters because the fallback is process-wide: two clients in
// one process cannot have different debug settings through it, and writing to
// it from a goroutine while another goroutine sets it is a data race. A logger
// bound with To has neither problem, which is why converted call sites take a
// sink from whoever constructed them.
package logger

import (
	"fmt"
	"io"
)

// Writer is the fallback destination for debug logs from loggers that were not
// given a sink of their own. If nil, those loggers produce no output.
//
// This is the global RFC 0022 §8 is removing. It remains only for the call
// sites still to be converted; do not add new uses.
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

// IsDebug reports whether the package-level fallback is enabled.
//
// It says nothing about loggers bound with To, which is why it is only
// meaningful to call sites still using the fallback.
func IsDebug() bool {
	return Writer != nil
}

// Debugf formats and writes a message to the fallback Writer if it is non-nil.
func Debugf(format string, args ...any) {
	if Writer != nil {
		_, _ = fmt.Fprintf(Writer, format+"\n", args...)
	}
}

// Logger allows component-specific prefixing for debug messages.
type Logger struct {
	prefix string
	w      io.Writer
}

// New returns a Logger with the specified component name and color, writing to
// the package-level Writer.
//
// Use it for the package-level default, then bind a sink with To when one is
// supplied — that keeps a component's prefix and color declared in one place
// rather than repeated at every construction site.
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
// A nil w yields a logger that falls back to the package-level Writer, so a
// caller that has no sink to offer can pass one through unconditionally
// instead of branching.
func (l *Logger) To(w io.Writer) *Logger {
	if l == nil {
		return nil
	}
	c := *l
	c.w = w
	return &c
}

// Debugf formats and prints a debug message to this logger's sink, or to the
// package-level Writer if it has none.
func (l *Logger) Debugf(format string, args ...any) {
	if l == nil {
		return
	}
	w := l.w
	if w == nil {
		w = Writer
	}
	if w != nil {
		_, _ = fmt.Fprintf(w, l.prefix+format+"\n", args...)
	}
}
