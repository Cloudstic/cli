package main

import (
	"fmt"
	"strings"

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
