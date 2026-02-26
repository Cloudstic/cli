package ui

import (
	"os"
	"time"

	"github.com/jedib0t/go-pretty/v6/progress"
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
	Log(msg string)
	Done()
	Error()
}

// ConsoleReporter implements Reporter using go-pretty for console output.
type ConsoleReporter struct{}

func NewConsoleReporter() *ConsoleReporter {
	return &ConsoleReporter{}
}

func (c *ConsoleReporter) StartPhase(name string, total int64, isBytes bool) Phase {
	pw := progress.NewWriter()
	pw.SetOutputWriter(os.Stdout)
	pw.SetAutoStop(true)
	pw.SetTrackerLength(25)
	pw.ShowOverallTracker(false)
	pw.ShowTime(true)
	pw.ShowTracker(true)
	pw.SetMessageWidth(20)
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

	go pw.Render()
	pw.AppendTracker(&tracker)

	return &consolePhase{
		pw:      pw,
		tracker: &tracker,
	}
}

type consolePhase struct {
	pw      progress.Writer
	tracker *progress.Tracker
}

func (cp *consolePhase) Increment(n int64) {
	cp.tracker.Increment(n)
}

func (cp *consolePhase) Log(msg string) {
	cp.pw.Log(msg)
}

func (cp *consolePhase) Done() {
	cp.tracker.MarkAsDone()
	// Allow render to catch up
	time.Sleep(time.Millisecond * 100)
	cp.pw.Stop()
}

func (cp *consolePhase) Error() {
	cp.tracker.MarkAsErrored()
	time.Sleep(time.Millisecond * 100)
	cp.pw.Stop()
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

func (np *noOpPhase) Increment(n int64) {}
func (np *noOpPhase) Log(msg string)    {}
func (np *noOpPhase) Done()             {}
func (np *noOpPhase) Error()            {}
