package tui

import (
	"fmt"
	"strings"
)

// Pure view-model derivation shared by the dashboard model: turning a
// ProfileCard into the labels, badges, and action buttons it renders. Nothing
// here emits escape sequences or measures a terminal — styling belongs to the
// lipgloss theme (styles.go) and layout to Bubble Tea/lipgloss (app.go).

// profileListWidths returns the name and badge column widths that align every
// row of the profile list.
func profileListWidths(profiles []ProfileCard) (nameWidth, badgeWidth int) {
	for _, profile := range profiles {
		if l := len([]rune(profile.Name)); l > nameWidth {
			nameWidth = l
		}
		labelWidth := len([]rune(plainProfileStateLabel(profile)))
		if labelWidth > badgeWidth {
			badgeWidth = labelWidth
		}
	}
	if badgeWidth > 0 {
		badgeWidth += 2 // brackets
	}
	return nameWidth, badgeWidth
}

func plainProfileStateLabel(profile ProfileCard) string {
	switch profile.Status {
	case ProfileStatusDisabled:
		return "disabled"
	case ProfileStatusWarning:
		return "warning"
	case ProfileStatusError:
		return "error"
	default:
		if profile.Enabled {
			return "enabled"
		}
		return "disabled"
	}
}

// profileHealthSummary reduces a profile's several health signals to the single
// most important thing the operator needs to know, worst first.
func profileHealthSummary(profile ProfileCard) string {
	switch {
	case profile.Status == ProfileStatusDisabled:
		return "disabled"
	case profile.Status == ProfileStatusError:
		return "configuration error"
	case profile.Reachability == StoreReachabilityUnavailable:
		return "store unavailable"
	case profile.Repository == RepositoryStateNotInitialized:
		return "repository not initialized"
	case profile.Reachability == StoreReachabilityPending:
		return "checking store"
	case profile.StoreHealth == StoreHealthMissingStore:
		return "missing store"
	case profile.StoreHealth == StoreHealthMissingAuth:
		return "missing auth"
	case profile.StoreHealth == StoreHealthProviderMismatch:
		return "provider mismatch"
	case profile.BackupState == BackupFreshnessStale:
		return "backup stale"
	default:
		return "ready"
	}
}

func profileStatusSummary(profile ProfileCard) string {
	if profile.Reachability == StoreReachabilityUnknown && profile.Repository == RepositoryStateUnknown {
		return "status unknown"
	}
	return ""
}

func backupFreshnessLabel(state BackupFreshness) string {
	switch state {
	case BackupFreshnessRecent:
		return "recent"
	case BackupFreshnessStale:
		return "stale"
	case BackupFreshnessNever:
		return "never"
	default:
		return ""
	}
}

// profileCheckSummary reports the outcome of the last `check` run, but only for
// the profile it was run against.
func profileCheckSummary(activity ActivityPanel, profile ProfileCard) string {
	if activity.ActionKind != ActionKindCheck || activity.Target != profile.Name {
		return ""
	}
	switch activity.Status {
	case ActivityStatusRunning:
		return "running"
	case ActivityStatusSuccess:
		if activity.UpdatedAt != "" {
			return fmt.Sprintf("passed at %s", activity.UpdatedAt)
		}
		return "passed"
	case ActivityStatusError:
		if activity.UpdatedAt != "" {
			return fmt.Sprintf("failed at %s", activity.UpdatedAt)
		}
		return "failed"
	default:
		return ""
	}
}

func trimSnapshotRef(ref string) string {
	return strings.TrimPrefix(ref, "snapshot/")
}

type actionButton struct {
	Key     string
	Label   string
	Enabled bool
	Reason  string
}

// selectedProfileActionButtons lists the buttons the Selection panel offers for
// a profile: its derived actions plus the always-available management keys.
func selectedProfileActionButtons(profile ProfileCard) []actionButton {
	buttons := make([]actionButton, 0, len(profile.Actions)+2)
	for _, action := range profile.Actions {
		buttons = append(buttons, actionButton{
			Key:     action.Key,
			Label:   actionButtonLabel(action),
			Enabled: action.Enabled,
			Reason:  action.Reason,
		})
	}
	buttons = append(buttons,
		actionButton{Key: "e", Label: "Edit profile", Enabled: true},
		actionButton{Key: "d", Label: "Delete profile", Enabled: true},
	)
	return buttons
}

func actionButtonLabel(action ProfileAction) string {
	switch action.Kind {
	case ActionKindInit:
		return "Initialize repository"
	case ActionKindCheck:
		return "Run check"
	default:
		if action.Enabled {
			return "Run backup"
		}
		return "Backup unavailable"
	}
}

// progressBarLine renders a fixed-width ASCII progress bar for the activity
// panel, or "" when the running phase reports no total to measure against.
func progressBarLine(activity ActivityPanel, width int) string {
	if activity.Total <= 0 || activity.Current < 0 || width <= 0 {
		return ""
	}
	current := min(activity.Current, activity.Total)
	filled := min(int(float64(current)/float64(activity.Total)*float64(width)), width)
	if filled < 0 {
		filled = 0
	}
	bar := strings.Repeat("=", filled) + strings.Repeat("-", width-filled)
	if activity.IsBytes {
		return fmt.Sprintf("[%s] %s / %s", bar, formatBytesLabel(current), formatBytesLabel(activity.Total))
	}
	return fmt.Sprintf("[%s] %d / %d", bar, current, activity.Total)
}

func formatBytesLabel(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.1f TiB", float64(b)/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
