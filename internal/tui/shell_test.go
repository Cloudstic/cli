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
				Name:        "documents",
				Source:      "local:/Users/test/Documents",
				StoreRef:    "remote",
				Enabled:     true,
				Status:      ProfileStatusReady,
				StoreHealth: StoreHealthReady,
				BackupState: BackupFreshnessRecent,
				LastBackup:  "2026-04-03 11:05",
				LastRef:     "snapshot/abc123",
				Actions: []ProfileAction{
					{Kind: ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true},
					{Kind: ActionKindCheck, Key: "c", Label: "Press c to run repository check", Enabled: true},
				},
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
		"Health",
		"ready",
		"Backup",
		"2026-04-03 11:05 (recent)",
		"Ref",
		"abc123",
		"No recent activity.",
		"Press c to run repository check",
		"Use ↑/↓ to select a profile. Press b to backup/init, c to check, q to quit.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in output:\n%s", want, got)
		}
	}
}
