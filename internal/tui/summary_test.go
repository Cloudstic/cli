package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// visibleColumn reports the printed column at which marker first appears in s,
// ignoring any styling escape sequences around it.
func visibleColumn(s, marker string) int {
	plain := ansiPattern.ReplaceAllString(s, "")
	idx := strings.Index(plain, marker)
	if idx < 0 {
		return -1
	}
	return lipgloss.Width(plain[:idx])
}

func TestProfileListWidths_AlignsBadgeColumn(t *testing.T) {
	profiles := []ProfileCard{
		{Name: "docs", Enabled: true, Status: ProfileStatusReady},
		{Name: "much-longer-name", Enabled: true, Status: ProfileStatusWarning},
	}

	nameWidth, badgeWidth := profileListWidths(profiles)
	if nameWidth != len("much-longer-name") {
		t.Fatalf("name width = %d want %d", nameWidth, len("much-longer-name"))
	}
	// "enabled" and "warning" are both 7 columns, plus the two brackets.
	if badgeWidth != len("warning")+2 {
		t.Fatalf("badge width = %d want %d", badgeWidth, len("warning")+2)
	}

	m := NewModel(Dashboard{Profiles: profiles})
	m.width = 100
	rows := []string{
		m.profileRow(profiles[0], false, nameWidth, badgeWidth),
		m.profileRow(profiles[1], true, nameWidth, badgeWidth),
	}
	first, second := visibleColumn(rows[0], "["), visibleColumn(rows[1], "[")
	if first <= 0 || first != second {
		t.Fatalf("badge columns differ: %d vs %d rows=%q", first, second, rows)
	}
}

func TestProfileHealthSummary_ReportsWorstSignalFirst(t *testing.T) {
	tests := []struct {
		name    string
		profile ProfileCard
		want    string
	}{
		{"disabled", ProfileCard{Status: ProfileStatusDisabled}, "disabled"},
		{"config error", ProfileCard{Status: ProfileStatusError}, "configuration error"},
		{"store down", ProfileCard{Reachability: StoreReachabilityUnavailable}, "store unavailable"},
		{"uninitialized", ProfileCard{Repository: RepositoryStateNotInitialized}, "repository not initialized"},
		{"probing", ProfileCard{Reachability: StoreReachabilityPending}, "checking store"},
		{"stale", ProfileCard{BackupState: BackupFreshnessStale}, "backup stale"},
		{"healthy", ProfileCard{Status: ProfileStatusReady, Enabled: true}, "ready"},
		{
			"unreachable outranks stale",
			ProfileCard{Reachability: StoreReachabilityUnavailable, BackupState: BackupFreshnessStale},
			"store unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := profileHealthSummary(tt.profile); got != tt.want {
				t.Fatalf("health = %q want %q", got, tt.want)
			}
		})
	}
}

func TestProfileCheckSummary_OnlyForTheCheckedProfile(t *testing.T) {
	activity := ActivityPanel{
		ActionKind: ActionKindCheck,
		Target:     "docs",
		Status:     ActivityStatusSuccess,
		UpdatedAt:  "2026-07-27 09:00:00",
	}
	if got := profileCheckSummary(activity, ProfileCard{Name: "docs"}); got != "passed at 2026-07-27 09:00:00" {
		t.Fatalf("check summary = %q", got)
	}
	if got := profileCheckSummary(activity, ProfileCard{Name: "photos"}); got != "" {
		t.Fatalf("check summary leaked to another profile: %q", got)
	}
	backup := ActivityPanel{ActionKind: ActionKindBackup, Target: "docs", Status: ActivityStatusSuccess}
	if got := profileCheckSummary(backup, ProfileCard{Name: "docs"}); got != "" {
		t.Fatalf("backup reported as check: %q", got)
	}
}

func TestSelectedProfileActionButtons_AppendsManagementKeys(t *testing.T) {
	profile := ProfileCard{
		Actions: deriveProfileActions(ProfileStatusReady, StoreHealthReady),
	}
	buttons := selectedProfileActionButtons(profile)
	if len(buttons) < 3 {
		t.Fatalf("buttons = %+v", buttons)
	}
	last := buttons[len(buttons)-2:]
	if last[0].Key != "e" || last[1].Key != "d" {
		t.Fatalf("management keys missing: %+v", buttons)
	}
	for _, b := range last {
		if !b.Enabled {
			t.Fatalf("management key %q should always be enabled", b.Key)
		}
	}
}

func TestProgressBarLine(t *testing.T) {
	tests := []struct {
		name     string
		activity ActivityPanel
		want     string
	}{
		{"no total", ActivityPanel{Current: 5}, ""},
		{"counts", ActivityPanel{Current: 5, Total: 10}, "[=====-----] 5 / 10"},
		{"clamped", ActivityPanel{Current: 20, Total: 10}, "[==========] 10 / 10"},
		{"bytes", ActivityPanel{Current: 2048, Total: 4096, IsBytes: true}, "[=====-----] 2.0 KiB / 4.0 KiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := progressBarLine(tt.activity, 10); got != tt.want {
				t.Fatalf("bar = %q want %q", got, tt.want)
			}
		})
	}
}

func TestTrimSnapshotRef(t *testing.T) {
	if got := trimSnapshotRef("snapshot/abc123"); got != "abc123" {
		t.Fatalf("trimmed = %q", got)
	}
	if got := trimSnapshotRef("abc123"); got != "abc123" {
		t.Fatalf("trimmed = %q", got)
	}
}
