package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// ActionUpdate is a single progress update from a running profile action.
// Non-final updates carry the live ActivityPanel; the final update sets Done
// (and Err when the action failed) and is the last value sent before the
// channel closes.
type ActionUpdate struct {
	Panel ActivityPanel
	Done  bool
	Err   error
}

// ActionRunner executes a profile action asynchronously. Start launches the
// action against ctx and returns a channel that streams progress updates and
// closes once the final (Done) update has been sent. Implementations must
// respect ctx cancellation so the model's cancel key can stop a running
// action.
type ActionRunner interface {
	Start(ctx context.Context, profile ProfileCard, kind ActionKind) <-chan ActionUpdate
}

// actionUpdateMsg carries one ActionUpdate plus the channel it came from so
// the model can re-subscribe for the next update without holding the channel
// in its own state.
type actionUpdateMsg struct {
	update ActionUpdate
	ch     <-chan ActionUpdate
}

// waitForAction blocks on the next update from ch and delivers it as a
// message. A closed channel is reported as a terminal Done update so the model
// always leaves the running state even if a runner forgets the final send.
func waitForAction(ch <-chan ActionUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-ch
		if !ok {
			return actionUpdateMsg{update: ActionUpdate{Done: true}, ch: nil}
		}
		return actionUpdateMsg{update: update, ch: ch}
	}
}

// startAction begins the action bound to the given key for the selected
// profile, if one is enabled and no action is already running. It returns the
// command that listens for the first progress update.
func (m Model) startAction(key string) (Model, tea.Cmd) {
	if m.running || m.runner == nil {
		return m, nil
	}
	profile, ok := m.currentProfile()
	if !ok {
		return m, nil
	}
	action, ok := enabledActionForKey(profile, key)
	if !ok {
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := m.runner.Start(ctx, profile, action.Kind)
	m.running = true
	m.cancel = cancel
	m.activity = ActivityPanel{
		Status:     ActivityStatusRunning,
		ActionKind: action.Kind,
		Target:     profile.Name,
	}
	return m, waitForAction(ch)
}

func enabledActionForKey(profile ProfileCard, key string) (ProfileAction, bool) {
	for _, action := range profile.Actions {
		if action.Key == key && action.Enabled {
			return action, true
		}
	}
	return ProfileAction{}, false
}

// cancelAction requests cancellation of the running action, if any. The action
// finishes on its own and delivers a terminal update; the model stays in the
// running state until that arrives.
func (m *Model) cancelAction() {
	if m.cancel != nil {
		m.cancel()
	}
}
