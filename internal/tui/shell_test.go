package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderDashboard(t *testing.T) {
	d := Dashboard{
		ProfileCount:    1,
		StoreCount:      1,
		AuthCount:       0,
		SelectedProfile: "documents",
		Activity: ActivityPanel{
			Status:     ActivityStatusSuccess,
			ActionKind: ActionKindCheck,
			Action:     "Run backup (profile documents)",
			Target:     "documents",
			Phase:      "Scanning",
			Current:    512,
			Total:      1024,
			IsBytes:    true,
			Summary:    "completed successfully",
			UpdatedAt:  "2026-04-03 15:05:00",
			Lines:      []string{"Snapshot abc123 saved"},
		},
		Profiles: []ProfileCard{
			{
				Name:         "documents",
				Source:       "local:/Users/test/Documents",
				StoreRef:     "remote",
				Enabled:      true,
				Status:       ProfileStatusReady,
				StoreHealth:  StoreHealthReady,
				Reachability: StoreReachabilityReachable,
				Repository:   RepositoryStateInitialized,
				BackupState:  BackupFreshnessRecent,
				LastBackup:   "2026-04-03 11:05",
				LastRef:      "snapshot/abc123",
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
	got := stripANSI(out.String())
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
		"Check",
		"passed at 2026-04-03 15:05:00",
		"success",
		"Run backup (profile documents)",
		"Scanning",
		"[==============--------------] 512 B / 1.0 KiB",
		"completed successfully",
		"2026-04-03 15:05:00",
		"Snapshot abc123 saved",
		"[b] Run backup",
		"[c] Run check",
		"[e] Edit profile",
		"[d] Delete profile",
		"Use ↑/↓ to select a profile. Press b to backup/init, c to check, n to create, e to edit, d to delete, q to quit.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in output:\n%s", want, got)
		}
	}
}

func TestRenderDashboardWithModal(t *testing.T) {
	d := Dashboard{
		ProfileCount: 1,
		StoreCount:   1,
		Profiles: []ProfileCard{{
			Name:     "documents",
			Source:   "local:/docs",
			StoreRef: "remote",
			Enabled:  true,
			Status:   ProfileStatusReady,
		}},
		Modal: &Modal{
			Title:    "Create Profile",
			Subtitle: "Configure the profile fields.",
			Hint:     "Enter to save, Esc to cancel.",
			Selected: 0,
			Fields: []ModalField{
				{Key: "name", Label: "Name", Kind: ModalFieldText, Value: "photos", Required: true},
				{Key: "source", Label: "Source", Kind: ModalFieldText, Value: "local:/photos", Required: true},
			},
		},
	}

	var out strings.Builder
	if err := RenderDashboardWidth(&out, d, 100); err != nil {
		t.Fatalf("RenderDashboardWidth: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Create Profile",
		"Configure the profile fields.",
		"Name",
		"Source",
		"photos",
		"local:/photos",
		"_",
		"Enter to save, Esc to cancel.",
		"Fields marked * are required.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Example:") {
		t.Fatalf("did not expect example when source field is not active:\n%s", got)
	}
}

func TestDashboardLinesWidth_TruncatesForNarrowTerminals(t *testing.T) {
	d := Dashboard{
		ProfileCount:    1,
		StoreCount:      1,
		SelectedProfile: "google-test",
		Activity: ActivityPanel{
			Status:  ActivityStatusSuccess,
			Action:  "Run backup (profile google-test)",
			Summary: "completed successfully",
			Lines:   []string{"Snapshot c9a98d85cd65e691c427554664c612c4014ff25572644e4ce4a158ecd593a773 saved"},
		},
		Profiles: []ProfileCard{
			{
				Name:         "google-test",
				Source:       "gdrive-changes:/Very Long Shared Drive Name/Extremely Long Folder Name",
				StoreRef:     "default-store",
				AuthRef:      "google-google-test",
				Enabled:      true,
				Status:       ProfileStatusReady,
				StoreHealth:  StoreHealthReady,
				Reachability: StoreReachabilityReachable,
				Repository:   RepositoryStateInitialized,
				BackupState:  BackupFreshnessRecent,
				LastBackup:   "2026-04-03 14:53",
				LastRef:      "snapshot/c9a98d85cd65e691c427554664c612c4014ff25572644e4ce4a158ecd593a773",
				Actions: []ProfileAction{
					{Kind: ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true},
					{Kind: ActionKindCheck, Key: "c", Label: "Press c to run repository check", Enabled: true},
				},
			},
		},
	}

	lines := dashboardLinesWidth(d, 72)
	for _, line := range lines {
		if got := visibleLen(line); got > 72 {
			t.Fatalf("line width=%d exceeds terminal width: %q", got, line)
		}
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "…") {
		t.Fatalf("expected truncated content in narrow layout:\n%s", got)
	}
}

func TestLayoutDashboardWidth_TracksProfileRowsAndActionRect(t *testing.T) {
	d := Dashboard{
		ProfileCount:    2,
		StoreCount:      1,
		SelectedProfile: "photos",
		Profiles: []ProfileCard{
			{Name: "docs", Enabled: true, Status: ProfileStatusReady},
			{
				Name:     "photos",
				Enabled:  true,
				Status:   ProfileStatusReady,
				Actions:  []ProfileAction{{Kind: ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true}},
				StoreRef: "remote",
				Source:   "local:/photos",
			},
		},
	}

	layout := LayoutDashboardWidth(d, 100)
	if len(layout.ProfileRows) != 2 {
		t.Fatalf("profile rows=%d want 2", len(layout.ProfileRows))
	}
	foundDocs := false
	foundPhotos := false
	for _, name := range layout.ProfileRows {
		if name == "docs" {
			foundDocs = true
		}
		if name == "photos" {
			foundPhotos = true
		}
	}
	if !foundDocs || !foundPhotos {
		t.Fatalf("unexpected profile row mapping: %+v", layout.ProfileRows)
	}
	if layout.ProfileRect.W <= 0 || layout.ProfileRect.H <= 0 {
		t.Fatalf("unexpected profile rect: %+v", layout.ProfileRect)
	}
	if layout.ProfileRect.X != 1 || layout.ProfileRect.Y <= 0 {
		t.Fatalf("unexpected profile rect origin: %+v", layout.ProfileRect)
	}
	if len(layout.ActionRows) != 3 {
		t.Fatalf("action rows=%d want 3", len(layout.ActionRows))
	}
	if layout.ActionRect.W <= 0 || layout.ActionRect.H <= 0 {
		t.Fatalf("unexpected action rect: %+v", layout.ActionRect)
	}
	if layout.ActionRect.X <= 0 || layout.ActionRect.Y <= 0 {
		t.Fatalf("unexpected action rect origin: %+v", layout.ActionRect)
	}
}

func TestRenderProfileList_AlignsStateBadges(t *testing.T) {
	d := Dashboard{
		SelectedProfile: "much-longer-name",
		Profiles: []ProfileCard{
			{Name: "docs", Enabled: true, Status: ProfileStatusReady},
			{Name: "much-longer-name", Enabled: true, Status: ProfileStatusWarning},
		},
	}

	lines := renderProfileList(d)
	if len(lines) != 2 {
		t.Fatalf("profile lines=%d want 2", len(lines))
	}
	docsIdx := visibleIndex(stripANSI(lines[0]), "[")
	longIdx := visibleIndex(stripANSI(lines[1]), "[")
	if docsIdx <= 0 || longIdx <= 0 {
		t.Fatalf("missing state badge in profile list: %+v", lines)
	}
	if docsIdx != longIdx {
		t.Fatalf("badge columns differ: docs=%d long=%d lines=%+v", docsIdx, longIdx, lines)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\x1b':
			inEscape = true
			i++
		case inEscape:
			if s[i] == 'm' {
				inEscape = false
			}
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

func visibleIndex(s, needle string) int {
	byteIdx := strings.Index(s, needle)
	if byteIdx < 0 {
		return -1
	}
	return utf8.RuneCountInString(s[:byteIdx])
}
