package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func richDashboard() Dashboard {
	return Dashboard{
		ProfileCount: 2, StoreCount: 1, AuthCount: 1,
		SelectedProfile: "docs",
		Profiles: []ProfileCard{
			{Name: "docs", Source: "local:/data", StoreRef: "b2", Enabled: true,
				Status: ProfileStatusReady, StoreHealth: StoreHealthReady,
				Reachability: StoreReachabilityReachable, Repository: RepositoryStateInitialized,
				BackupState: BackupFreshnessRecent, LastBackup: "2026-07-24 08:12",
				LastRef: "snapshot/abc123",
				Actions: deriveProfileActions(ProfileStatusReady, StoreHealthReady)},
			{Name: "photos", Source: "gdrive://Photos", StoreRef: "s3", AuthRef: "g", Enabled: true,
				Status: ProfileStatusWarning, StoreHealth: StoreHealthUnavailable,
				Reachability: StoreReachabilityUnavailable},
		},
	}
}

func TestView_ShowsTitledPanels(t *testing.T) {
	m := NewModel(richDashboard())
	m.width = 100
	view := m.View()
	for _, title := range []string{"Overview", "Profiles", "Selection", "Activity"} {
		if !strings.Contains(view, title) {
			t.Fatalf("view missing panel title %q\n%s", title, view)
		}
	}
}

func TestView_ShowsSummaryRowsAndActionButtons(t *testing.T) {
	m := NewModel(richDashboard())
	m.width = 100
	view := m.View()
	for _, want := range []string{
		"State", "Source", "Store", "Health", "Backup", "Ref",
		"[b] Run backup", "[c] Run check", "[e] Edit profile", "[d] Delete profile",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("summary/actions missing %q\n%s", want, view)
		}
	}
}

func TestView_WarningProfileShowsHealthNote(t *testing.T) {
	d := richDashboard()
	d.SelectedProfile = "photos"
	m := NewModel(d)
	m.width = 100
	view := m.View()
	if !strings.Contains(view, "store unavailable") {
		t.Fatalf("warning profile should surface health note\n%s", view)
	}
}

// Resize arrives as a tea.WindowSizeMsg on every platform, which is what
// replaced the SIGWINCH handler and its no-op Windows stub (issue #341).
func TestView_WindowSizeMsgDrivesLayout(t *testing.T) {
	m := NewModel(richDashboard())

	wide, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	wideView := wide.View()
	if wide.(Model).width != 120 {
		t.Fatalf("width = %d want 120", wide.(Model).width)
	}

	narrow, _ := wide.(Model).Update(tea.WindowSizeMsg{Width: 68, Height: 40})
	narrowView := narrow.View()
	if narrow.(Model).width != 68 {
		t.Fatalf("width = %d want 68", narrow.(Model).width)
	}
	if wideView == narrowView {
		t.Fatalf("resize did not change the rendered layout\n%s", narrowView)
	}
	if lipgloss.Width(wideView) <= lipgloss.Width(narrowView) {
		t.Fatalf("wide view (%d) should be wider than narrow view (%d)",
			lipgloss.Width(wideView), lipgloss.Width(narrowView))
	}
}

func TestView_NarrowWidthStacksPanels(t *testing.T) {
	m := NewModel(richDashboard())
	m.width = 68
	view := m.View()
	// Both panel titles present, but stacked (each on its own set of rows):
	// no line should contain both "Profiles" and "Selection".
	if !strings.Contains(view, "Profiles") || !strings.Contains(view, "Selection") {
		t.Fatalf("narrow view missing a panel\n%s", view)
	}
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, "Profiles") && strings.Contains(line, "Selection") {
			t.Fatalf("panels should be stacked, not side by side, at width 68\n%s", view)
		}
	}
}

func TestView_ActivityPanelAlwaysPresent(t *testing.T) {
	m := NewModel(richDashboard())
	m.width = 100
	if !strings.Contains(m.View(), "No recent activity.") {
		t.Fatalf("idle activity panel should show a placeholder\n%s", m.View())
	}
	m.activity = ActivityPanel{Status: ActivityStatusRunning, Action: "Run backup", Phase: "scanning"}
	view := m.View()
	for _, want := range []string{"Status", "running", "Action", "Run backup", "Phase", "scanning"} {
		if !strings.Contains(view, want) {
			t.Fatalf("running activity missing %q\n%s", want, view)
		}
	}
}

func TestView_FooterShowsContextualHints(t *testing.T) {
	m := NewModel(richDashboard()).WithForms(&stubFormsBackend{}).WithRunner(&stubRunner{})
	m.width = 100
	view := m.View()
	for _, want := range []string{"backup", "check", "new", "edit", "delete", "quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("footer missing hint %q\n%s", want, view)
		}
	}
}
