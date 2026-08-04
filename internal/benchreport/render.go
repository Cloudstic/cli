package benchreport

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Render writes the whole report: prose, table, then charts.
//
// The table comes before the charts and is always emitted. GitHub renders
// mermaid in job summaries, but `xychart-beta` needs a recent enough Mermaid,
// and a renderer that does not know the directive shows the block as source
// text — so the numbers must already be on the page by the time that happens.
func Render(w io.Writer, rep *Report, title string) error {
	b := &strings.Builder{}

	fmt.Fprintf(b, "## %s\n\n", title)

	switch rep.Kind() {
	case KindScaling:
		b.WriteString("Peak resident set size of the real binary, per operation, at each tree\n" +
			"size. A row that stays flat holds a working set; one that tracks the file\n" +
			"count holds a per-entry structure, and will eventually meet a repository it\n" +
			"cannot open.\n\n")
		renderScalingTable(b, rep)
	case KindComparison:
		if len(rep.Tools()) > 1 {
			b.WriteString("Each tool over the same dataset. Times on a shared runner carry several\n" +
				"percent of noise, so read the column for its order of magnitude rather than\n" +
				"its last digit.\n\n")
		} else {
			b.WriteString("One pass over a mixed dataset. Times on a shared runner carry several\n" +
				"percent of noise, so read them for their order of magnitude rather than\n" +
				"their last digit; the bytes added are exact.\n\n")
		}
		renderComparisonTable(b, rep)
	}

	// A comparison of one tool has nothing to chart — a single bar compares
	// nothing — so the heading is written only if something follows it.
	charts := &strings.Builder{}
	renderCharts(charts, rep)
	if charts.Len() > 0 {
		b.WriteString("\n### Charts\n")
		b.WriteString(charts.String())
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// ---------------------------------------------------------------------------
// Tables
// ---------------------------------------------------------------------------

func renderScalingTable(b *strings.Builder, rep *Report) {
	scales := rep.Scales()

	b.WriteString("| Operation |")
	for _, s := range scales {
		fmt.Fprintf(b, " %s files |", humanInt(s))
	}
	b.WriteString(" Growth |\n|---|")
	for range scales {
		b.WriteString("---:|")
	}
	b.WriteString("---:|\n")

	for _, op := range rep.Operations() {
		fmt.Fprintf(b, "| `%s` |", op)
		var first, last float64
		var haveFirst bool
		for _, s := range scales {
			row, ok := rep.find("", op, s)
			if !ok {
				b.WriteString(" - |")
				continue
			}
			fmt.Fprintf(b, " %s MB |", trim(row.PeakMB))
			if !haveFirst {
				first, haveFirst = row.PeakMB, true
			}
			last = row.PeakMB
		}
		if haveFirst && first > 0 {
			fmt.Fprintf(b, " %.2fx |\n", last/first)
		} else {
			b.WriteString(" - |\n")
		}
	}
}

func renderComparisonTable(b *strings.Builder, rep *Report) {
	tools := rep.Tools()
	scale := rep.Scales()[0]

	// Repository growth is only shown for a single-tool run. Across tools it
	// would invite a comparison the number cannot support — each writes a
	// different format, so "bytes added" is not measuring the same thing twice.
	withDelta := len(tools) == 1 && rep.hasRepoDelta()

	b.WriteString("| Operation |")
	for _, t := range tools {
		fmt.Fprintf(b, " %s |", t)
	}
	if withDelta {
		b.WriteString(" Repo added |")
	}
	b.WriteString("\n|---|")
	for range tools {
		b.WriteString("---:|")
	}
	if withDelta {
		b.WriteString("---:|")
	}
	b.WriteString("\n")

	for _, op := range rep.Operations() {
		fmt.Fprintf(b, "| `%s` |", op)
		var delta string
		for _, t := range tools {
			row, ok := rep.find(t, op, scale)
			if !ok {
				b.WriteString(" - |")
				continue
			}
			fmt.Fprintf(b, " %ss · %s MB |", trim(row.Seconds), trim(row.PeakMB))
			delta = row.RepoDelta
		}
		if withDelta {
			if delta == "" {
				delta = "-"
			}
			fmt.Fprintf(b, " %s |", delta)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nEach cell is wall time and peak RSS.\n")
}

// ---------------------------------------------------------------------------
// Charts
// ---------------------------------------------------------------------------

// renderCharts emits one chart per operation, never one chart with several
// series: mermaid's xychart has no legend, so a multi-series chart cannot say
// which line is which. A chart titled with its operation needs no legend.
//
// The y-axis is shared across every chart of a metric so the panels are
// comparable — a per-chart axis makes a flat operation and a climbing one look
// identical.
func renderCharts(b *strings.Builder, rep *Report) {
	switch rep.Kind() {
	case KindScaling:
		ymax := axisMax(rep, PeakMB)
		xs := make([]string, 0, len(rep.Scales()))
		for _, s := range rep.Scales() {
			xs = append(xs, strconv.Itoa(s))
		}
		for _, op := range rep.Operations() {
			var vals []string
			for _, s := range rep.Scales() {
				row, ok := rep.find("", op, s)
				if !ok {
					continue
				}
				vals = append(vals, trim(row.PeakMB))
			}
			chart(b, fmt.Sprintf("%s — peak RSS (MB)", op), "files in tree", "peak RSS (MB)", xs, vals, ymax, "line")
		}
	case KindComparison:
		ymax := axisMax(rep, Seconds)
		tools := rep.Tools()
		scale := rep.Scales()[0]
		for _, op := range rep.Operations() {
			var xs, vals []string
			for _, t := range tools {
				row, ok := rep.find(t, op, scale)
				if !ok {
					continue
				}
				xs = append(xs, t)
				vals = append(vals, trim(row.Seconds))
			}
			if len(vals) < 2 {
				continue // a single bar compares nothing
			}
			chart(b, fmt.Sprintf("%s — time (s)", op), "tool", "seconds", xs, vals, ymax, "bar")
		}
	}
}

func chart(b *strings.Builder, title, xLabel, yLabel string, xs, vals []string, ymax float64, mark string) {
	if len(vals) == 0 {
		return
	}
	b.WriteString("\n```mermaid\nxychart-beta\n")
	fmt.Fprintf(b, "    title %q\n", title)
	fmt.Fprintf(b, "    x-axis %q [%s]\n", xLabel, strings.Join(quoteAll(xs), ", "))
	fmt.Fprintf(b, "    y-axis %q 0 --> %s\n", yLabel, trim(ymax))
	fmt.Fprintf(b, "    %s [%s]\n", mark, strings.Join(vals, ", "))
	b.WriteString("```\n")
}

// axisMax leaves headroom above the largest value so the top point is not
// clipped against the frame.
func axisMax(rep *Report, m Metric) float64 {
	var max float64
	for _, row := range rep.Rows {
		if v := m.Value(row); v > max {
			max = v
		}
	}
	return float64(int(max*1.15)) + 1
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

// quoteAll quotes any label that is not a bare number. Mermaid needs quotes
// around a category with a space or a dash in it, and rejects them nowhere.
func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		if _, err := strconv.Atoi(x); err == nil {
			out[i] = x
			continue
		}
		out[i] = fmt.Sprintf("%q", x)
	}
	return out
}

// trim renders a float without trailing zeros, so a table reads 154 rather
// than 154.000000.
func trim(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// humanInt groups thousands so 50000 reads as 50,000 in a table heading.
func humanInt(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
