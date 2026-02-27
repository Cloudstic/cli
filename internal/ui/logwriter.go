package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jedib0t/go-pretty/v6/progress"
)

// SafeLogWriter is an io.Writer that coexists with go-pretty progress bars.
// When a progress writer is active, lines are routed through pw.Log() so they
// render above the bar. Otherwise lines go straight to stderr.
type SafeLogWriter struct {
	mu sync.Mutex
	pw progress.Writer
}

// SetActive registers the progress writer that is currently rendering.
func (w *SafeLogWriter) SetActive(pw progress.Writer) {
	w.mu.Lock()
	w.pw = pw
	w.mu.Unlock()
}

// ClearActive unregisters the progress writer.
func (w *SafeLogWriter) ClearActive() {
	w.mu.Lock()
	w.pw = nil
	w.mu.Unlock()
}

func (w *SafeLogWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	pw := w.pw
	w.mu.Unlock()

	msg := strings.TrimRight(string(p), "\n")
	if msg == "" {
		return len(p), nil
	}

	if pw != nil {
		pw.Log(msg)
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
	return len(p), nil
}
