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
	ActionRect  Rect
}

func RenderDashboard(w io.Writer, d Dashboard) error {
	return RenderDashboardWidth(w, d, 0)
}

func RenderDashboardWidth(w io.Writer, d Dashboard, width int) error {
	if _, err := fmt.Fprintf(w, "%s%s%s\n", ui.Bold, "Cloudstic TUI", ui.Reset); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%sOperator dashboard for profiles, stores, and auth.%s\n", ui.Dim, ui.Reset); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	stats := []string{
		fmt.Sprintf("%sProfiles%s %d", ui.Cyan, ui.Reset, d.ProfileCount),
		fmt.Sprintf("%sStores%s %d", ui.Cyan, ui.Reset, d.StoreCount),
		fmt.Sprintf("%sAuth%s %d", ui.Cyan, ui.Reset, d.AuthCount),
	}
	if err := renderBoxExact(w, "Overview", []string{strings.Join(stats, "   ")}, panelWidth(width)); err != nil {
		return err
	}

	profilesWidth, detailWidth := splitPaneWidths(width)
	leftLines := renderProfileList(d)
	rightLines := renderSelectedProfile(d)
	leftLines, rightLines = equalizePaneHeights(leftLines, rightLines)
	if err := renderColumns(w,
		boxLinesExact("Profiles", leftLines, profilesWidth),
		boxLinesExact("Selection", rightLines, detailWidth),
		width,
	); err != nil {
		return err
	}

	activity := d.ActivityLines
	if len(activity) == 0 {
		activity = []string{fmt.Sprintf("%sNo recent activity.%s", ui.Dim, ui.Reset)}
	}
	if err := renderBoxExact(w, "Activity", activity, panelWidth(width)); err != nil {
		return err
	}

	_, err := fmt.Fprintf(w, "\n%sUse ↑/↓ to select a profile. Press b to backup/init, c to check, q to quit.%s\n", ui.Dim, ui.Reset)
	return err
}

func LayoutDashboardWidth(d Dashboard, width int) DashboardLayout {
	layout := DashboardLayout{ProfileRows: map[int]string{}}
	y := 1
	y += 3 // title, subtitle, blank
	y += len(boxLinesExact("Overview", []string{
		fmt.Sprintf("%sProfiles%s %d   %sStores%s %d   %sAuth%s %d", ui.Cyan, ui.Reset, d.ProfileCount, ui.Cyan, ui.Reset, d.StoreCount, ui.Cyan, ui.Reset, d.AuthCount),
	}, panelWidth(width)))

	profilesWidth, detailWidth := splitPaneWidths(width)
	leftLines := renderProfileList(d)
	rightLines := renderSelectedProfile(d)
	leftLines, rightLines = equalizePaneHeights(leftLines, rightLines)
	leftBox := boxLinesExact("Profiles", leftLines, profilesWidth)

	rightStartX := longestVisible(leftBox) + 3
	contentStartY := y + 3
	for i, profile := range d.Profiles {
		layout.ProfileRows[contentStartY+i] = profile.Name
	}
	actionRow := len(rightLines) - 1
	if actionRow >= 0 {
		layout.ActionRect = Rect{
			X: rightStartX + 2,
			Y: contentStartY + actionRow,
			W: detailWidth,
			H: 1,
		}
	}
	return layout
}

func renderBoxExact(w io.Writer, title string, lines []string, width int) error {
	for _, line := range boxLinesExact(title, lines, width) {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
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

func renderColumns(w io.Writer, left, right []string, maxWidth int) error {
	leftWidth := longestVisible(left)
	rightWidth := longestVisible(right)
	height := len(left)
	if len(right) > height {
		height = len(right)
	}
	for i := 0; i < height; i++ {
		leftLine := paddedLine(left, i, leftWidth)
		rightLine := paddedLine(right, i, rightWidth)
		if _, err := fmt.Fprintf(w, "%s  %s\n", leftLine, rightLine); err != nil {
			return err
		}
	}
	return nil
}

func renderProfileList(d Dashboard) []string {
	if len(d.Profiles) == 0 {
		return []string{fmt.Sprintf("%sNo profiles configured.%s", ui.Dim, ui.Reset)}
	}
	lines := make([]string, 0, len(d.Profiles))
	for _, profile := range d.Profiles {
		lines = append(lines, profileHeaderLine(profile, profile.Name == d.SelectedProfile))
	}
	return lines
}

func renderSelectedProfile(d Dashboard) []string {
	profile, ok := selectedProfileCard(d)
	if !ok {
		return []string{fmt.Sprintf("%sNo profile selected.%s", ui.Dim, ui.Reset)}
	}
	lines := []string{
		fmt.Sprintf("%s%s%s", ui.Bold, profile.Name, ui.Reset),
		profileDetailLine("State", plainProfileStateLabel(profile)),
		profileDetailLine("Source", profile.Source),
		profileDetailLine("Store", profile.StoreRef),
		profileDetailLine("Health", storeHealthLabel(profile.StoreHealth)),
	}
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
	if profile.StatusNote != "" && (profile.Status != ProfileStatusReady || profile.BackupState != BackupFreshnessNever) {
		lines = append(lines, profileDetailLine("Status", profile.StatusNote))
	}
	lines = append(lines, "")
	for _, action := range profile.Actions {
		lines = append(lines, fmt.Sprintf("%sAction%s  %s", ui.Dim, ui.Reset, actionLabel(action)))
	}
	return lines
}

func profileHeaderLine(profile ProfileCard, selected bool) string {
	prefix := "  "
	if selected {
		prefix = fmt.Sprintf("%s› %s", ui.Cyan, ui.Reset)
	}
	return fmt.Sprintf("%s%s%s%s  [%s]", prefix, ui.Bold, profile.Name, ui.Reset, profileStateLabel(profile))
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

func storeHealthLabel(health StoreHealth) string {
	switch health {
	case StoreHealthReady:
		return "ready"
	case StoreHealthPending:
		return "pending"
	case StoreHealthDisabled:
		return "disabled"
	case StoreHealthMissingStore:
		return "missing store"
	case StoreHealthMissingAuth:
		return "missing auth"
	case StoreHealthProviderMismatch:
		return "provider mismatch"
	case StoreHealthUnavailable:
		return "unavailable"
	case StoreHealthNotInitialized:
		return "repository not initialized"
	default:
		return "unknown"
	}
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

func actionLabel(action ProfileAction) string {
	if action.Enabled {
		return action.Label
	}
	return fmt.Sprintf("%s%s%s", ui.Dim, action.Label, ui.Reset)
}

func trimSnapshotRef(ref string) string {
	return strings.TrimPrefix(ref, "snapshot/")
}
