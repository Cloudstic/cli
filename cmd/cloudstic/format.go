package main

import (
	"fmt"
	"strings"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/jedib0t/go-pretty/v6/table"
)

// formatBytes returns a human-readable representation of a byte count.
func formatBytes(b int64) string {
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

// renderSnapshotTable prints a table of snapshot entries to w. If reasons
// is non-nil, a "Reasons" column is appended with the value for each ref.
func (r *runner) renderSnapshotTable(entries []engine.SnapshotEntry, reasons map[string]string) {
	t := table.NewWriter()
	t.SetOutputMirror(r.out)

	header := table.Row{"Seq", "Created", "Snapshot Hash", "Source", "Account", "Path", "Tags"}
	if reasons != nil {
		header = append(header, "Reasons")
	}
	t.AppendHeader(header)

	for _, e := range entries {
		var source, account, path string
		if e.Snap.Source != nil {
			source = e.Snap.Source.Type
			driveName := e.Snap.Source.DriveName
			if driveName == "" {
				driveName = e.Snap.Source.VolumeLabel
			}
			if driveName != "" {
				source += " (" + driveName + ")"
			}
			account = e.Snap.Source.Account
			path = e.Snap.Source.Path
		} else if e.Snap.Meta != nil {
			source = e.Snap.Meta["source"]
		}

		hash := strings.TrimPrefix(e.Ref, "snapshot/")
		tags := strings.Join(e.Snap.Tags, ", ")

		row := table.Row{e.Snap.Seq, e.Snap.Created, hash, source, account, path, tags}
		if reasons != nil {
			row = append(row, reasons[e.Ref])
		}
		t.AppendRow(row)
	}

	t.Render()
}

// sourceGroupKey returns a string key that groups snapshots by source identity.
func sourceGroupKey(s *core.SourceInfo) string {
	if s == nil {
		return ""
	}
	pathToken := s.Path
	if s.PathID != "" {
		pathToken = s.PathID
	}
	if s.Identity != "" {
		return s.Type + "\x00" + s.Identity + "\x00" + pathToken
	}
	if s.VolumeUUID != "" {
		return s.Type + "\x00" + s.VolumeUUID + "\x00" + pathToken
	}
	return s.Type + "\x00" + s.Account + "\x00" + pathToken
}

// sourceGroupLabel returns a human-readable label for a source group.
func sourceGroupLabel(s *core.SourceInfo) string {
	if s == nil {
		return "(unknown source)"
	}
	var parts []string
	label := s.Type
	driveName := s.DriveName
	if driveName == "" {
		driveName = s.VolumeLabel
	}
	if driveName != "" {
		label += " (" + driveName + ")"
	}
	parts = append(parts, label)
	if s.Account != "" {
		parts = append(parts, s.Account)
	}
	if s.Path != "" {
		parts = append(parts, s.Path)
	}
	return strings.Join(parts, " · ")
}

// renderGroupedSnapshotTables prints one table per source group.
func (r *runner) renderGroupedSnapshotTables(entries []engine.SnapshotEntry) {
	// Collect groups preserving first-seen order.
	type group struct {
		key     string
		label   string
		entries []engine.SnapshotEntry
	}
	var groups []group
	idx := map[string]int{}

	for _, e := range entries {
		k := sourceGroupKey(e.Snap.Source)
		if i, ok := idx[k]; ok {
			groups[i].entries = append(groups[i].entries, e)
		} else {
			idx[k] = len(groups)
			groups = append(groups, group{
				key:     k,
				label:   sourceGroupLabel(e.Snap.Source),
				entries: []engine.SnapshotEntry{e},
			})
		}
	}

	for i, g := range groups {
		if i > 0 {
			_, _ = fmt.Fprintln(r.out)
		}
		_, _ = fmt.Fprintf(r.out, "── %s (%d snapshots)\n", g.label, len(g.entries))
		r.renderSnapshotTable(g.entries, nil)
	}
}
