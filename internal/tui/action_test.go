package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// stubRunner is a scripted ActionRunner: it emits the given updates in order,
// records the kind it was asked to run, and cancels via ctx.
type stubRunner struct {
	updates  []ActionUpdate
	started  ActionKind
	canceled bool
}

func (s *stubRunner) Start(ctx context.Context, _ ProfileCard, kind ActionKind) <-chan ActionUpdate {
	s.started = kind
	ch := make(chan ActionUpdate, len(s.updates)+1)
	go func() {
		defer close(ch)
		for _, u := range s.updates {
			ch <- u
		}
		// If nothing terminal was scripted, watch for cancellation.
		if len(s.updates) == 0 {
			<-ctx.Done()
			s.canceled = true
			ch <- ActionUpdate{Panel: ActivityPanel{Status: ActivityStatusError, Summary: "canceled"}, Done: true, Err: ctx.Err()}
		}
	}()
	return ch
}

func TestModel_StartAction_RunsAndCompletes(t *testing.T) {
	runner := &stubRunner{updates: []ActionUpdate{
		{Panel: ActivityPanel{Status: ActivityStatusRunning, Action: "Run backup", Phase: "scanning"}},
		{Panel: ActivityPanel{Status: ActivityStatusSuccess, Summary: "completed"}, Done: true},
	}}
	m := NewModel(testDashboard()).WithRunner(runner)

	// Press "b": on the ready "alpha" profile this maps to a backup action.
	next, cmd := m.Update(keyRunes("b"))
	m = next.(Model)
	if !m.running {
		t.Fatalf("model should be running after starting action")
	}
	if runner.started != ActionKindBackup {
		t.Fatalf("started kind=%q want backup", runner.started)
	}

	// Pump the streamed updates through the model.
	for i := 0; i < 5 && cmd != nil; i++ {
		msg := cmd()
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(Model)
	}
	if m.running {
		t.Fatalf("model still running after terminal update")
	}
	if m.activity.Status != ActivityStatusSuccess {
		t.Fatalf("activity status=%q want success", m.activity.Status)
	}
	if !strings.Contains(m.View(), "done") {
		t.Fatalf("view missing completion state\n%s", m.View())
	}
}

func TestModel_StartAction_IgnoredWhenAlreadyRunning(t *testing.T) {
	runner := &stubRunner{} // blocks until canceled
	m := NewModel(testDashboard()).WithRunner(runner)

	next, _ := m.Update(keyRunes("b"))
	m = next.(Model)
	if !m.running {
		t.Fatalf("expected running")
	}
	// A second action key while running must be a no-op (no new command).
	next, cmd := m.Update(keyRunes("c"))
	m = next.(Model)
	if cmd != nil {
		t.Fatalf("second action while running should not start a command")
	}
}

func TestModel_CancelKey_CancelsRunningAction(t *testing.T) {
	runner := &stubRunner{} // waits for ctx cancellation
	m := NewModel(testDashboard()).WithRunner(runner)

	next, cmd := m.Update(keyRunes("b"))
	m = next.(Model)
	if !m.running {
		t.Fatalf("expected running")
	}

	// Press "x" to cancel.
	next, _ = m.Update(keyRunes("x"))
	m = next.(Model)

	// The wait command should now observe the terminal (canceled) update.
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)
	if m.running {
		t.Fatalf("model should stop running after cancellation")
	}
	if !runner.canceled {
		t.Fatalf("runner ctx was not canceled")
	}
}

func TestModel_StartAction_NoRunnerIsNoOp(t *testing.T) {
	m := NewModel(testDashboard())
	next, cmd := m.Update(keyRunes("b"))
	m = next.(Model)
	if m.running || cmd != nil {
		t.Fatalf("action without a runner must be a no-op")
	}
}
