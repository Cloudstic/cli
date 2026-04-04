package tui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/cloudstic/cli/internal/ui"
)

type Rect struct {
	X int
	Y int
	W int
	H int
}

type DashboardLayout struct {
	ProfileRows map[int]string
	ProfileRect Rect
	ActionRows  map[int]string
	ActionRect  Rect
}

func RenderDashboard(w io.Writer, d Dashboard) error {
	return RenderDashboardWidth(w, d, 0)
}

func RenderDashboardWidth(w io.Writer, d Dashboard, width int) error {
	lines := dashboardLinesWidth(d, width)
	dimBackground := d.Modal != nil
	for _, line := range lines {
		if dimBackground {
			line = dimmedLine(line)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	if d.Modal != nil {
		if err := renderModalOverlay(w, *d.Modal, width, len(lines)); err != nil {
			return err
		}
	}
	return nil
}

func dimmedLine(line string) string {
	return ui.Dim + strings.ReplaceAll(line, ui.Reset, ui.Reset+ui.Dim) + ui.Reset
}

func LayoutDashboardWidth(d Dashboard, width int) DashboardLayout {
	layout := DashboardLayout{
		ProfileRows: map[int]string{},
		ActionRows:  map[int]string{},
	}
	y := 1
	y += 3 // title, subtitle, blank
	y += len(boxLinesExact("Overview", []string{
		fmt.Sprintf("%sProfiles%s %d   %sStores%s %d   %sAuth%s %d", ui.Cyan, ui.Reset, d.ProfileCount, ui.Cyan, ui.Reset, d.StoreCount, ui.Cyan, ui.Reset, d.AuthCount),
	}, panelWidth(width)))

	profilesWidth, detailWidth := splitPaneWidths(width)
	leftLines := renderProfileList(d)
	rightLines, actionRows := renderSelectedProfile(d)
	leftLines, rightLines = equalizePaneHeights(leftLines, rightLines)
	leftBox := boxLinesExact("Profiles", leftLines, profilesWidth)
	leftWidth := longestVisible(leftBox)
	layout.ProfileRect = Rect{
		X: 1,
		Y: y,
		W: leftWidth,
		H: len(leftBox),
	}

	rightStartX := leftWidth + 3
	contentStartY := y + 3
	for i, profile := range d.Profiles {
		layout.ProfileRows[contentStartY+i] = profile.Name
	}
	if len(actionRows) > 0 {
		layout.ActionRect = Rect{
			X: rightStartX + 2,
			Y: contentStartY,
			W: detailWidth,
			H: len(rightLines),
		}
		for row, key := range actionRows {
			layout.ActionRows[contentStartY+row] = key
		}
	}
	return layout
}

func dashboardLinesWidth(d Dashboard, width int) []string {
	lines := []string{
		fmt.Sprintf("%s%s%s", ui.Bold, "Cloudstic TUI", ui.Reset),
		fmt.Sprintf("%sOperator dashboard for profiles, stores, and auth.%s", ui.Dim, ui.Reset),
		"",
	}

	stats := []string{
		fmt.Sprintf("%sProfiles%s %d", ui.Cyan, ui.Reset, d.ProfileCount),
		fmt.Sprintf("%sStores%s %d", ui.Cyan, ui.Reset, d.StoreCount),
		fmt.Sprintf("%sAuth%s %d", ui.Cyan, ui.Reset, d.AuthCount),
	}
	lines = append(lines, boxLinesExact("Overview", []string{strings.Join(stats, "   ")}, panelWidth(width))...)

	profilesWidth, detailWidth := splitPaneWidths(width)
	leftLines := renderProfileList(d)
	rightLines, _ := renderSelectedProfile(d)
	leftLines, rightLines = equalizePaneHeights(leftLines, rightLines)
	lines = append(lines, renderColumnLines(
		boxLinesExact("Profiles", leftLines, profilesWidth),
		boxLinesExact("Selection", rightLines, detailWidth),
	)...)

	lines = append(lines, boxLinesExact("Activity", renderActivityPanel(d.Activity), panelWidth(width))...)
	footer := fmt.Sprintf("%sUse ↑/↓ to select a profile. Press s/h to switch views, b to backup/init, c to check, n to create, e to edit, d to delete, q to quit.%s", ui.Dim, ui.Reset)
	if width > 0 {
		footer = truncateVisible(footer, width)
	}
	lines = append(lines, "", footer)
	return lines
}

func boxLinesExact(title string, lines []string, width int) []string {
	titleLine := fmt.Sprintf("%s%s%s", ui.Bold, title, ui.Reset)
	if width <= 0 {
		width = visibleLen(titleLine)
		for _, line := range lines {
			if l := visibleLen(line); l > width {
				width = l
			}
		}
	}
	if width < visibleLen(titleLine) {
		width = visibleLen(titleLine)
	}
	innerWidth := width + 2
	out := []string{"┌" + strings.Repeat("─", innerWidth) + "┐"}
	titlePadding := width - visibleLen(titleLine)
	out = append(out, fmt.Sprintf("│ %s%s │", titleLine, strings.Repeat(" ", titlePadding)))
	if len(lines) > 0 {
		out = append(out, fmt.Sprintf("│ %s │", strings.Repeat(" ", width)))
	}
	for _, line := range lines {
		line = truncateVisible(line, width)
		padding := width - visibleLen(line)
		out = append(out, fmt.Sprintf("│ %s%s │", line, strings.Repeat(" ", padding)))
	}
	out = append(out, "└"+strings.Repeat("─", innerWidth)+"┘")
	return out
}

func renderColumnLines(left, right []string) []string {
	leftWidth := longestVisible(left)
	rightWidth := longestVisible(right)
	height := len(left)
	if len(right) > height {
		height = len(right)
	}
	lines := make([]string, 0, height)
	for i := 0; i < height; i++ {
		leftLine := paddedLine(left, i, leftWidth)
		rightLine := paddedLine(right, i, rightWidth)
		lines = append(lines, fmt.Sprintf("%s  %s", leftLine, rightLine))
	}
	return lines
}

func renderProfileList(d Dashboard) []string {
	if len(d.Profiles) == 0 {
		return []string{fmt.Sprintf("%sNo profiles configured.%s", ui.Dim, ui.Reset)}
	}
	nameWidth, badgeWidth := profileListWidths(d.Profiles)
	lines := make([]string, 0, len(d.Profiles))
	for _, profile := range d.Profiles {
		lines = append(lines, profileHeaderLine(profile, profile.Name == d.SelectedProfile, nameWidth, badgeWidth))
	}
	return lines
}

func renderActivityPanel(activity ActivityPanel) []string {
	lines := []string{}
	if activity.Status != ActivityStatusIdle {
		lines = append(lines, profileDetailLine("Status", activityStatusLabel(activity.Status)))
	}
	if activity.Action != "" {
		lines = append(lines, profileDetailLine("Action", activity.Action))
	}
	if activity.Phase != "" {
		lines = append(lines, profileDetailLine("Phase", activity.Phase))
	}
	if bar := progressBarLine(activity, 28); bar != "" {
		lines = append(lines, profileDetailLine("Progress", bar))
	}
	if activity.Summary != "" {
		lines = append(lines, profileDetailLine("Result", activity.Summary))
	}
	if activity.UpdatedAt != "" {
		lines = append(lines, profileDetailLine("Updated", activity.UpdatedAt))
	}
	if len(lines) > 0 && len(activity.Lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, activity.Lines...)
	if len(lines) == 0 {
		return []string{fmt.Sprintf("%sNo recent activity.%s", ui.Dim, ui.Reset)}
	}
	return lines
}

func renderSelectedProfile(d Dashboard) ([]string, map[int]string) {
	profile, ok := selectedProfileCard(d)
	if !ok {
		return []string{fmt.Sprintf("%sNo profile selected.%s", ui.Dim, ui.Reset)}, nil
	}
	lines := []string{
		fmt.Sprintf("%s%s%s", ui.Bold, profile.Name, ui.Reset),
		renderProfileViewTabs(d.SelectedView),
		"",
	}
	if d.SelectedView == ProfileViewHistory {
		lines = append(lines, renderProfileHistory(profile)...)
		return appendProfileActionButtons(lines, profile)
	}
	lines = append(lines,
		profileDetailLine("State", plainProfileStateLabel(profile)),
		profileDetailLine("Source", profile.Source),
		profileDetailLine("Store", profile.StoreRef),
		profileDetailLine("Health", profileHealthSummary(profile)),
	)
	if profile.AuthRef != "" {
		lines = append(lines, profileDetailLine("Auth", profile.AuthRef))
	}
	switch {
	case profile.LastBackup != "":
		backupValue := profile.LastBackup
		if label := backupFreshnessLabel(profile.BackupState); label != "" {
			backupValue = fmt.Sprintf("%s (%s)", backupValue, label)
		}
		lines = append(lines, profileDetailLine("Backup", backupValue))
	case profile.Status == ProfileStatusReady && profile.BackupState == BackupFreshnessNever:
		lines = append(lines, profileDetailLine("Backup", "never backed up"))
	}
	if profile.LastRef != "" {
		lines = append(lines, profileDetailLine("Ref", trimSnapshotRef(profile.LastRef)))
	}
	if check := profileCheckSummary(d.Activity, profile); check != "" {
		lines = append(lines, profileDetailLine("Check", check))
	}
	if note := profileStatusSummary(profile); note != "" {
		lines = append(lines, profileDetailLine("Status", note))
	}
	if profile.StatusNote != "" && noteAddsContext(profile) {
		lines = append(lines, profileDetailLine("Status", profile.StatusNote))
	}
	return appendProfileActionButtons(lines, profile)
}

func renderModalOverlay(w io.Writer, modal Modal, screenWidth, screenHeight int) error {
	startX, width := modalLayout(screenWidth)
	lines := modalLines(modal, width)
	startY := 4
	if screenHeight > 0 {
		startY = ((screenHeight - len(lines)) / 2) + 1
		if startY < 4 {
			startY = 4
		}
	}
	for i, line := range lines {
		if _, err := fmt.Fprintf(w, "\x1b[%d;%dH%s", startY+i, startX, line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\x1b[%d;%dH", startY+len(lines), 1)
	return err
}

func modalLines(modal Modal, width int) []string {
	lines := []string{}
	if modal.Subtitle != "" {
		lines = append(lines, fmt.Sprintf("%s%s%s", ui.Dim, modal.Subtitle, ui.Reset), "")
	}
	labelWidth := modalLabelWidth(modal)
	for i, field := range modal.Fields {
		selected := i == modal.Selected
		hasError := modal.ErrorField == field.Key && modal.Error != ""
		prefix := "  "
		if selected {
			prefix = fmt.Sprintf("%s› %s", ui.Cyan, ui.Reset)
		}
		labelText := field.Label
		if field.Required && !field.Disabled {
			labelText += " " + ui.Yellow + "*" + ui.Reset
		}
		label := fmt.Sprintf("%s%s%s", ui.Dim, labelText, ui.Reset)
		if selected {
			label = fmt.Sprintf("%s%s%s", ui.Cyan, labelText, ui.Reset)
		}
		if hasError {
			label = labelText
		}
		padding := labelWidth - visibleLen(label)
		if padding < 0 {
			padding = 0
		}
		lines = append(lines, fmt.Sprintf("%s%s%s  %s", prefix, label, strings.Repeat(" ", padding), modalFieldValue(field, selected)))
		if hasError {
			lines = append(lines, fmt.Sprintf("  %s%s%s", ui.Red, modal.Error, ui.Reset))
		}
	}
	if len(modal.Message) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, modal.Message...)
	}
	if modal.Error != "" && modal.ErrorField == "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, fmt.Sprintf("%s%s%s", ui.Red, modal.Error, ui.Reset))
	}
	if modal.Hint != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, fmt.Sprintf("%sFields marked * are required.%s", ui.Dim, ui.Reset))
		lines = append(lines, fmt.Sprintf("%s%s%s", ui.Dim, modal.Hint, ui.Reset))
	}
	return boxLinesExact(modal.Title, lines, width)
}

func modalLabelWidth(modal Modal) int {
	width := 0
	for _, field := range modal.Fields {
		label := field.Label
		if field.Required && !field.Disabled {
			label += " *"
		}
		if l := len(label); l > width {
			width = l
		}
	}
	return width
}

func modalFieldValue(field ModalField, selected bool) string {
	if field.Disabled {
		return fmt.Sprintf("%snot required%s", ui.Dim, ui.Reset)
	}
	switch field.Kind {
	case ModalFieldSelect:
		value := field.Value
		if value == "" {
			value = "none"
		}
		if selected {
			return fmt.Sprintf("%s<%s>%s  %s←/→%s", ui.Cyan, value, ui.Reset, ui.Dim, ui.Reset)
		}
		return fmt.Sprintf("[%s]", value)
	default:
		value := field.Value
		if value == "" {
			value = fmt.Sprintf("%s<empty>%s", ui.Dim, ui.Reset)
		}
		if selected {
			cursor := fmt.Sprintf("%s_%s", ui.Cyan, ui.Reset)
			return fmt.Sprintf("%s%s%s", value, cursor, "")
		}
		return value
	}
}

func modalLayout(screenWidth int) (startX int, width int) {
	if screenWidth <= 0 {
		return 1, 60
	}
	leftWidth, rightWidth := splitPaneWidths(screenWidth)
	startX = leftWidth + 7
	width = rightWidth
	if width < 40 {
		width = 40
	}
	maxWidth := screenWidth - startX - 4
	if maxWidth < width {
		width = maxWidth
	}
	if width < 40 {
		width = screenWidth - 12
		if width < 40 {
			width = 40
		}
		startX = ((screenWidth - (width + 4)) / 2) + 1
		if startX < 1 {
			startX = 1
		}
	}
	return startX, width
}

func profileHeaderLine(profile ProfileCard, selected bool, nameWidth, badgeWidth int) string {
	prefix := "  "
	if selected {
		prefix = fmt.Sprintf("%s› %s", ui.Cyan, ui.Reset)
	}
	namePadding := nameWidth - visibleLen(profile.Name)
	if namePadding < 0 {
		namePadding = 0
	}
	return fmt.Sprintf("%s%s%s%s%s  %s", prefix, ui.Bold, profile.Name, ui.Reset, strings.Repeat(" ", namePadding), profileStateBadge(profile, badgeWidth))
}

func profileListWidths(profiles []ProfileCard) (nameWidth, badgeWidth int) {
	for _, profile := range profiles {
		if l := visibleLen(profile.Name); l > nameWidth {
			nameWidth = l
		}
		labelWidth := visibleLen(plainProfileStateLabel(profile))
		if labelWidth > badgeWidth {
			badgeWidth = labelWidth
		}
	}
	if badgeWidth > 0 {
		badgeWidth += 2 // brackets
	}
	return nameWidth, badgeWidth
}

func profileDetailLine(label, value string) string {
	return fmt.Sprintf("  %s%-6s%s  %s", ui.Dim, label, ui.Reset, value)
}

func splitPaneWidths(total int) (int, int) {
	if total <= 0 {
		total = 100
	}
	available := total - 10 // two box borders/padding (+4 each) plus 2 spaces between columns
	if available < 40 {
		available = 40
	}
	left := available / 3
	if left < 24 {
		left = 24
	}
	right := available - left
	if right < 36 {
		right = 36
	}
	if left+right > available {
		left = available - right
		if left < 24 {
			left = 24
			right = available - left
		}
	}
	return left, right
}

func panelWidth(total int) int {
	if total <= 0 {
		total = 100
	}
	width := total - 4
	if width < 20 {
		return 20
	}
	return width
}

func longestVisible(lines []string) int {
	width := 0
	for _, line := range lines {
		if l := visibleLen(line); l > width {
			width = l
		}
	}
	return width
}

func paddedLine(lines []string, idx, width int) string {
	if idx >= len(lines) {
		return strings.Repeat(" ", width)
	}
	line := lines[idx]
	padding := width - visibleLen(line)
	if padding < 0 {
		padding = 0
	}
	return line + strings.Repeat(" ", padding)
}

func equalizePaneHeights(left, right []string) ([]string, []string) {
	target := len(left)
	if len(right) > target {
		target = len(right)
	}
	for len(left) < target {
		left = append(left, "")
	}
	for len(right) < target {
		right = append(right, "")
	}
	return left, right
}

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
	switch {
	case profile.Reachability == StoreReachabilityUnknown && profile.Repository == RepositoryStateUnknown:
		return "status unknown"
	default:
		return ""
	}
}

func noteAddsContext(profile ProfileCard) bool {
	if profile.StatusNote == "" {
		return false
	}
	summary := profileHealthSummary(profile)
	return !strings.EqualFold(profile.StatusNote, summary)
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

func activityStatusLabel(status ActivityStatus) string {
	switch status {
	case ActivityStatusRunning:
		return "running"
	case ActivityStatusSuccess:
		return "success"
	case ActivityStatusError:
		return "failed"
	default:
		return ""
	}
}

func progressBarLine(activity ActivityPanel, width int) string {
	if activity.Total <= 0 || activity.Current < 0 || width <= 0 {
		return ""
	}
	current := activity.Current
	if current > activity.Total {
		current = activity.Total
	}
	ratio := float64(current) / float64(activity.Total)
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}
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

func visibleLen(s string) int {
	n := 0
	inEscape := false
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\x1b':
			inEscape = true
			i++
		case inEscape && s[i] == 'm':
			inEscape = false
			i++
		case !inEscape:
			_, size := utf8.DecodeRuneInString(s[i:])
			n++
			i += size
		default:
			i++
		}
	}
	return n
}

func truncateVisible(s string, limit int) string {
	if limit <= 0 || visibleLen(s) <= limit {
		return s
	}
	if limit == 1 {
		return "…"
	}
	var b strings.Builder
	visible := 0
	inEscape := false
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\x1b':
			inEscape = true
			b.WriteByte(s[i])
			i++
		case inEscape:
			b.WriteByte(s[i])
			if s[i] == 'm' {
				inEscape = false
			}
			i++
		default:
			if visible >= limit-1 {
				b.WriteRune('…')
				b.WriteString(ui.Reset)
				return b.String()
			}
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteRune(r)
			visible++
			i += size
		}
	}
	return b.String()
}

func profileStateLabel(profile ProfileCard) string {
	switch profile.Status {
	case ProfileStatusDisabled:
		return fmt.Sprintf("%sdisabled%s", ui.Dim, ui.Reset)
	case ProfileStatusWarning:
		return fmt.Sprintf("%swarning%s", ui.Cyan, ui.Reset)
	case ProfileStatusError:
		return "error"
	default:
		if profile.Enabled {
			return fmt.Sprintf("%senabled%s", ui.Green, ui.Reset)
		}
		return fmt.Sprintf("%sdisabled%s", ui.Dim, ui.Reset)
	}
}

func profileStateBadge(profile ProfileCard, width int) string {
	label := profileStateLabel(profile)
	plainWidth := visibleLen(plainProfileStateLabel(profile))
	padding := width - plainWidth - 2
	if padding < 0 {
		padding = 0
	}
	return fmt.Sprintf("[%s%s]", label, strings.Repeat(" ", padding))
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

func selectedProfileCard(d Dashboard) (ProfileCard, bool) {
	for _, profile := range d.Profiles {
		if profile.Name == d.SelectedProfile {
			return profile, true
		}
	}
	if len(d.Profiles) == 0 {
		return ProfileCard{}, false
	}
	return d.Profiles[0], true
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

func renderProfileViewTabs(selected ProfileView) string {
	summary := "[s] Summary"
	history := "[h] History"
	switch selected {
	case ProfileViewSummary:
		summary = fmt.Sprintf("%s[s] Summary%s", ui.Cyan, ui.Reset)
	case ProfileViewHistory:
		history = fmt.Sprintf("%s[h] History%s", ui.Cyan, ui.Reset)
	}
	return fmt.Sprintf("  %s  %s", summary, history)
}

func renderProfileHistory(profile ProfileCard) []string {
	if len(profile.History) == 0 {
		return []string{
			fmt.Sprintf("%sNo snapshots found for this profile.%s", ui.Dim, ui.Reset),
		}
	}
	lines := []string{
		fmt.Sprintf("%sRecent snapshots for this profile:%s", ui.Dim, ui.Reset),
	}
	limit := len(profile.History)
	if limit > 8 {
		limit = 8
	}
	for i := 0; i < limit; i++ {
		snapshot := profile.History[i]
		lines = append(lines, fmt.Sprintf("  %s  %s", snapshot.Created, trimSnapshotRef(snapshot.Ref)))
	}
	return lines
}

func appendProfileActionButtons(lines []string, profile ProfileCard) ([]string, map[int]string) {
	buttons := selectedProfileActionButtons(profile)
	actionRows := map[int]string{}
	if len(buttons) > 0 {
		lines = append(lines, "")
		for _, button := range buttons {
			if button.Enabled {
				actionRows[len(lines)] = button.Key
			}
			lines = append(lines, renderActionButton(button))
			if !button.Enabled && button.Reason != "" {
				lines = append(lines, fmt.Sprintf("  %s%s%s", ui.Dim, button.Reason, ui.Reset))
			}
		}
	}
	return lines, actionRows
}

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

func renderActionButton(button actionButton) string {
	key := fmt.Sprintf("%s[%s]%s", ui.Cyan, button.Key, ui.Reset)
	label := button.Label
	if button.Enabled {
		return fmt.Sprintf("  %s %s", key, label)
	}
	return fmt.Sprintf("  %s %s%s%s", key, ui.Dim, label, ui.Reset)
}
