package tui

import (
	"context"
	"time"

	"github.com/cloudstic/cli/pkg/profile"

	tea "github.com/charmbracelet/bubbletea"
)

// StoreProbeTimeout bounds a single store health probe so one unreachable
// backend cannot stall the dashboard. Probes run concurrently, so the whole
// first paint is bounded by this regardless of how many stores are configured.
const StoreProbeTimeout = 15 * time.Second

// StoreProber checks the health of a single store. Implementations must honor
// ctx (including its deadline) and must not block past it.
type StoreProber interface {
	Probe(ctx context.Context, name string, store profile.Store) StoreProbe
}

// probeResultMsg reports the outcome of one store probe back into the model.
type probeResultMsg struct {
	name  string
	probe StoreProbe
}

// probeStoreCmd probes a single store under a per-store timeout and reports the
// result. Batching several of these yields concurrent, bounded probing.
func probeStoreCmd(prober StoreProber, name string, store profile.Store) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), StoreProbeTimeout)
		defer cancel()
		return probeResultMsg{name: name, probe: prober.Probe(ctx, name, store)}
	}
}

// probeAllCmd returns a command that probes every configured store
// concurrently. Each store resolves independently, so the model can render
// "checking…" badges immediately and update them as results arrive.
func (m Model) probeAllCmd() tea.Cmd {
	if m.cfg == nil || m.prober == nil || len(m.cfg.Stores) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(m.cfg.Stores))
	for name, store := range m.cfg.Stores {
		cmds = append(cmds, probeStoreCmd(m.prober, name, store))
	}
	return tea.Batch(cmds...)
}

// probeSelectedStoreCmd re-probes the store backing the selected profile. It is
// used to refresh the dashboard after an action (e.g. init) changes store
// state.
func (m Model) probeSelectedStoreCmd() tea.Cmd {
	if m.cfg == nil || m.prober == nil {
		return nil
	}
	profile, ok := m.currentProfile()
	if !ok {
		return nil
	}
	store, ok := m.cfg.Stores[profile.StoreRef]
	if !ok {
		return nil
	}
	return probeStoreCmd(m.prober, profile.StoreRef, store)
}
