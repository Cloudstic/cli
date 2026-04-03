package tui

import (
	"strings"
	"testing"
)

func TestRenderDashboard(t *testing.T) {
	d := Dashboard{
		ProfileCount:    1,
		StoreCount:      1,
		AuthCount:       0,
		SelectedProfile: "documents",
		Profiles: []ProfileCard{
			{
				Name:       "documents",
				Source:     "local:/Users/test/Documents",
				StoreRef:   "remote",
				Enabled:    true,
				Status:     "ready",
				LastBackup: "2026-04-03 11:05",
				LastRef:    "snapshot/abc123",
			},
		},
	}

	var out strings.Builder
	if err := RenderDashboard(&out, d); err != nil {
		t.Fatalf("RenderDashboard: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Cloudstic TUI",
		"Operator dashboard for profiles, stores, and auth.",
		"Overview",
		"Profiles",
		"Activity",
		"Stores",
		"Auth",
		"documents",
		"›",
		"enabled",
		"Source",
		"local:/Users/test/Documents",
		"Store",
		"remote",
		"Backup",
		"2026-04-03 11:05",
		"Ref",
		"abc123",
		"No recent activity.",
		"Use ↑/↓ to select a profile. Press b to backup or init. Press q to quit.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in output:\n%s", want, got)
		}
	}
}
