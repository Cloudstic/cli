package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/jedib0t/go-pretty/v6/progress"
)

// Detail is how much an operation should say about what it is doing.
//
// It exists because verbosity is a presentation choice, and the code that makes
// presentation choices is the reporter — not the engine producing the events.
// Every operation used to carry its own verbose flag and gate its own log calls,
// which meant six option constructors for one idea and six places to change it.
type Detail int

const (
	// DetailNormal reports milestones: phases, counts, warnings.
	DetailNormal Detail = iota
	// DetailVerbose additionally reports per-item progress — every object
	// verified, every file restored. On a large repository this is a line per
	// object, which is why it is off by default and why Logf defers formatting.
	DetailVerbose
)

// Reporter defines the interface for progress reporting.
type Reporter interface {
	// StartPhase starts a new progress tracking phase.
	// name: Description of the phase.
	// total: Total items/bytes to process. 0 for indeterminate.
	// isBytes: If true, units are bytes, otherwise count.
	StartPhase(name string, total int64, isBytes bool) Phase
}

// Phase represents an active progress tracking phase.
type Phase interface {
	Increment(n int64)
	// Log records a message the caller has already decided to emit.
	Log(msg string)
	// Logf records a message at a detail level, formatting the arguments only
	// if the reporter wants that level.
	//
	// The deferral is the point, not an optimization detail: per-item logging
	// runs once per object checked or file restored, so formatting a string the
	// reporter will discard is work proportional to repository size.
	Logf(level Detail, format string, args ...any)
	Done()
	Error()
}

// ConsoleReporter implements Reporter using go-pretty for console output.
type ConsoleReporter struct {
	logWriter *SafeLogWriter
	detail    Detail
}

func NewConsoleReporter() *ConsoleReporter {
	return &ConsoleReporter{}
}

// SetDetail chooses how much this reporter shows.
//
// This is the single place verbosity is decided. Operations report what they are
// doing at a level; this decides which levels reach the user. Before it existed,
// each of the nine operations carried its own verbose option and gated its own
// log calls, so the same choice had nine names and nine implementations.
func (c *ConsoleReporter) SetDetail(d Detail) { c.detail = d }

// SetLogWriter registers a SafeLogWriter that will be kept in sync with the
// active progress writer so external log lines (e.g. store debug output)
// render cleanly above the progress bar.
func (c *ConsoleReporter) SetLogWriter(w *SafeLogWriter) {
	c.logWriter = w
}

func (c *ConsoleReporter) StartPhase(name string, total int64, isBytes bool) Phase {
	pw := progress.NewWriter()
	pw.SetOutputWriter(os.Stdout)
	pw.SetAutoStop(true)
	pw.SetTrackerLength(25)
	pw.Style().Visibility.TrackerOverall = false
	pw.Style().Visibility.Time = true
	pw.Style().Visibility.Tracker = true
	pw.SetMessageLength(20)
	pw.SetNumTrackersExpected(1)
	pw.SetStyle(progress.StyleDefault)
	pw.SetTrackerPosition(progress.PositionRight)
	pw.SetUpdateFrequency(time.Millisecond * 100)
	pw.Style().Colors = progress.StyleColorsExample
	pw.Style().Options.PercentFormat = "%4.1f%%"

	units := progress.UnitsDefault
	if isBytes {
		units = progress.UnitsBytes
	}

	tracker := progress.Tracker{Message: name, Total: total, Units: units}

	if c.logWriter != nil {
		c.logWriter.SetActive(pw)
	}

	go pw.Render()
	pw.AppendTracker(&tracker)

	return &consolePhase{
		pw:        pw,
		tracker:   &tracker,
		logWriter: c.logWriter,
		detail:    c.detail,
	}
}

type consolePhase struct {
	pw        progress.Writer
	tracker   *progress.Tracker
	logWriter *SafeLogWriter
	detail    Detail
}

func (cp *consolePhase) Increment(n int64) {
	cp.tracker.Increment(n)
}

func (cp *consolePhase) Log(msg string) {
	cp.pw.Log(msg)
}

func (cp *consolePhase) Logf(level Detail, format string, args ...any) {
	if level > cp.detail {
		return
	}
	cp.pw.Log(fmt.Sprintf(format, args...))
}

func (cp *consolePhase) Done() {
	cp.tracker.MarkAsDone()
	time.Sleep(time.Millisecond * 100)
	cp.pw.Stop()
	if cp.logWriter != nil {
		cp.logWriter.ClearActive()
	}
}

func (cp *consolePhase) Error() {
	cp.tracker.MarkAsErrored()
	time.Sleep(time.Millisecond * 100)
	cp.pw.Stop()
	if cp.logWriter != nil {
		cp.logWriter.ClearActive()
	}
}

// NoOpReporter implements Reporter doing nothing (for tests or silent mode).
type NoOpReporter struct{}

func NewNoOpReporter() *NoOpReporter {
	return &NoOpReporter{}
}

func (n *NoOpReporter) StartPhase(name string, total int64, isBytes bool) Phase {
	return &noOpPhase{}
}

type noOpPhase struct{}

func (np *noOpPhase) Increment(n int64)           {}
func (np *noOpPhase) Log(msg string)              {}
func (np *noOpPhase) Logf(Detail, string, ...any) {}
func (np *noOpPhase) Done()                       {}
func (np *noOpPhase) Error()                      {}
