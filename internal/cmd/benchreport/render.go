package main

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
		b.WriteString("Measurements of the real binary, per operation, at each tree size. A row\n" +
			"that stays flat holds a working set; one that tracks the file count holds a\n" +
			"per-entry structure, and will eventually meet a repository it cannot open.\n\n")
		writeSampleNote(b, rep)

		b.WriteString("### Peak RSS (MB)\n\n")
		b.WriteString("The largest amount live at once — what decides whether an operation fits\n" +
			"in the memory available.\n\n")
		renderScalingTable(b, rep, PeakMB)

		// Peak RSS is blind to allocation that is freed again, which is most of
		// it: a change removing tens of MB of garbage leaves the high-water mark
		// where it was. The two columns answer different questions and neither
		// substitutes for the other.
		if rep.hasAlloc() {
			b.WriteString("\n### Total allocated (MB)\n\n")
			b.WriteString("Cumulative bytes allocated, freed or not. Peak RSS cannot see churn that\n" +
				"the collector reclaims, so this is the column that moves when an operation\n" +
				"stops allocating something it did not need.\n\n")
			renderScalingTable(b, rep, AllocMB)
		}

		// Against MinIO, request latency rather than bandwidth sets the pace,
		// and egress is billed — so these are the columns that matter for a
		// remote store, the way peak RSS matters for a local one.
		if rep.hasS3() {
			b.WriteString("\n### Requests\n\n")
			b.WriteString("How many calls each operation made against the backend — the number\n" +
				"that decides whether an operation is usable over a network at all.\n\n")
			renderScalingTable(b, rep, Requests)

			b.WriteString("\n### Sent (MB)\n\n")
			b.WriteString("Bytes the backend sent back — downloads on a restore or check, a\n" +
				"pack body cache undersized enough to force re-reads.\n\n")
			renderScalingTable(b, rep, SentMB)
		}
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
		writeSampleNote(b, rep)
		renderComparisonTable(b, rep)
	}

	renderRepoSummary(b, rep)
	renderByAPI(b, rep)

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

// writeSampleNote explains the median and the band, but only for a harness
// that actually repeated itself. Saying "median of 1 run" would advertise a
// precision the numbers do not have.
func writeSampleNote(b *strings.Builder, rep *Report) {
	n := rep.Samples()
	if n < 2 {
		return
	}
	fmt.Fprintf(b, "Each point is the median of %d runs, with the min–max band in\n"+
		"parentheses. A difference smaller than that band is noise, not a result.\n\n", n)
}

// ---------------------------------------------------------------------------
// Tables
// ---------------------------------------------------------------------------

func renderScalingTable(b *strings.Builder, rep *Report, m Metric) {
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
			c, ok := rep.cell("", op, s)
			stat := m.Value(c)
			if !ok || stat.N == 0 {
				b.WriteString(" - |")
				continue
			}
			fmt.Fprintf(b, " %s |", statCell(stat))
			if !haveFirst {
				first, haveFirst = stat.Median, true
			}
			last = stat.Median
		}
		// Growth compares medians. Comparing the extremes instead would report
		// the worst case at one end against the best at the other and turn the
		// sampling noise into apparent scaling.
		if haveFirst && first > 0 {
			fmt.Fprintf(b, " %.2fx |\n", last/first)
		} else {
			b.WriteString(" - |\n")
		}
	}
}

// statCell renders a measurement as its median, followed by the observed band
// when repeated samples actually disagreed. A point measured once, or one whose
// samples landed on the same number, prints as a bare value rather than an
// unhelpful "148.9 (148.9–148.9)".
func statCell(s Stat) string {
	if s.N < 2 || s.Spread() < 0.05 {
		return trim(s.Median)
	}
	return fmt.Sprintf("%s (%s–%s)", trim(s.Median), trim(s.Min), trim(s.Max))
}

func renderComparisonTable(b *strings.Builder, rep *Report) {
	tools := rep.Tools()
	scale := rep.Scales()[0]

	// Repository growth is only shown for a single-tool run. Across tools it
	// would invite a comparison the number cannot support — each writes a
	// different format, so "bytes added" is not measuring the same thing twice.
	withDelta := len(tools) == 1 && rep.hasRepoDelta()

	// Allocation is gated the same way, for a stronger version of the same
	// reason: only cloudstic reports it at all, so a cross-tool column would be
	// one number and a row of dashes. A single-size memory run lands here, and
	// it is the column that shows churn peak RSS cannot.
	withAlloc := len(tools) == 1 && rep.hasAlloc()

	// Requests and bytes are gated the same way again: only a MinIO run
	// measures them, and a cross-tool comparison never points at a backend
	// that reports its own counters, so this and withAlloc never both apply
	// to a run this harness produces — but the gate stays independent of
	// withAlloc for the same reason withAlloc is independent of withDelta.
	withRequests := len(tools) == 1 && rep.hasS3()

	b.WriteString("| Operation |")
	for _, t := range tools {
		fmt.Fprintf(b, " %s |", t)
	}
	if withAlloc {
		b.WriteString(" Allocated |")
	}
	if withDelta {
		b.WriteString(" Repo added |")
	}
	if withRequests {
		b.WriteString(" Requests | Sent |")
	}
	b.WriteString("\n|---|")
	for range tools {
		b.WriteString("---:|")
	}
	if withAlloc {
		b.WriteString("---:|")
	}
	if withDelta {
		b.WriteString("---:|")
	}
	if withRequests {
		b.WriteString("---:|---:|")
	}
	b.WriteString("\n")

	for _, op := range rep.Operations() {
		fmt.Fprintf(b, "| `%s` |", op)
		var delta, alloc, requests, sentMB string
		for _, t := range tools {
			c, ok := rep.cell(t, op, scale)
			if !ok {
				b.WriteString(" - |")
				continue
			}
			fmt.Fprintf(b, " %ss · %s MB |", statCell(c.Seconds), statCell(c.PeakMB))
			delta = c.RepoDelta
			if c.HasAlloc {
				alloc = statCell(c.AllocMB) + " MB"
			}
			if c.HasS3 {
				requests = statCell(c.Requests)
				sentMB = statCell(c.SentMB) + " MB"
			}
		}
		if withAlloc {
			if alloc == "" {
				alloc = "-"
			}
			fmt.Fprintf(b, " %s |", alloc)
		}
		if withDelta {
			if delta == "" {
				delta = "-"
			}
			fmt.Fprintf(b, " %s |", delta)
		}
		if withRequests {
			if requests == "" {
				requests = "-"
			}
			if sentMB == "" {
				sentMB = "-"
			}
			fmt.Fprintf(b, " %s | %s |", requests, sentMB)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nEach cell is wall time and peak RSS.")
	if withAlloc {
		b.WriteString(" `Allocated` is cumulative bytes allocated,\nfreed or not — the column that moves when an operation stops allocating\nsomething it did not need.")
	}
	if withRequests {
		b.WriteString(" `Sent` is bytes the backend sent back —\ndownloads on a restore or check.")
	}
	b.WriteString("\n")
}

// renderRepoSummary renders the final repository size and the logical/stored
// ratio, when a self-benchmark recorded them.
//
// These are not timed operations — they have no seconds or peak_mb worth
// showing — so they get their own small section rather than a row of zeros in
// the per-operation tables above. A scaling sweep gets one row per tree size;
// a single-size run gets two plain lines, since a one-row table of "5,000
// files" tells the reader nothing a bare sentence would not.
func renderRepoSummary(b *strings.Builder, rep *Report) {
	if !rep.hasSummaryRows() {
		return
	}
	scales := rep.Scales()

	b.WriteString("\n### Repository\n\n")

	if len(scales) <= 1 {
		var scale int
		if len(scales) == 1 {
			scale = scales[0]
		}
		if c, ok := rep.cell("", OpFinalRepoSize, scale); ok && c.RepoDelta != "" {
			fmt.Fprintf(b, "Final repository size: **%s**\n\n", c.RepoDelta)
		}
		if c, ok := rep.cell("", OpLogicalStored, scale); ok && c.RepoDelta != "" {
			fmt.Fprintf(b, "Logical / stored: **%s**\n\n", c.RepoDelta)
		}
		return
	}

	b.WriteString("| Tree size | Final repo size | Logical / stored |\n|---|---:|---:|\n")
	for _, s := range scales {
		fmt.Fprintf(b, "| %s files |", humanInt(s))

		if c, ok := rep.cell("", OpFinalRepoSize, s); ok && c.RepoDelta != "" {
			fmt.Fprintf(b, " %s |", c.RepoDelta)
		} else {
			b.WriteString(" - |")
		}
		if c, ok := rep.cell("", OpLogicalStored, s); ok && c.RepoDelta != "" {
			fmt.Fprintf(b, " %s |\n", c.RepoDelta)
		} else {
			b.WriteString(" - |\n")
		}
	}
}

// renderByAPI collapses the per-API request breakdown into a details block.
//
// It is long — every API name and its count, for every operation and, on a
// scaling sweep, every size — and would dominate a summary whose headline
// numbers are the Requests and Sent columns above it. A reader who wants the
// breakdown opens the block; one who does not is not made to scroll past it.
func renderByAPI(b *strings.Builder, rep *Report) {
	if !rep.hasS3() {
		return
	}
	scaling := rep.Kind() == KindScaling

	var rows strings.Builder
	any := false
	for _, op := range rep.Operations() {
		for _, s := range rep.Scales() {
			c, ok := rep.cell("", op, s)
			if !ok || c.ByAPI == "" {
				continue
			}
			any = true
			if scaling {
				fmt.Fprintf(&rows, "| `%s` | %s files | %s |\n", op, humanInt(s), c.ByAPI)
			} else {
				fmt.Fprintf(&rows, "| `%s` | %s |\n", op, c.ByAPI)
			}
		}
	}
	if !any {
		return
	}

	b.WriteString("\n<details>\n<summary>Requests by API</summary>\n\n")
	if scaling {
		b.WriteString("| Operation | Size | By API |\n|---|---|---|\n")
	} else {
		b.WriteString("| Operation | By API |\n|---|---|\n")
	}
	b.WriteString(rows.String())
	b.WriteString("\n</details>\n")
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
		metrics := []Metric{PeakMB}
		if rep.hasAlloc() {
			metrics = append(metrics, AllocMB)
		}
		xs := make([]string, 0, len(rep.Scales()))
		for _, s := range rep.Scales() {
			xs = append(xs, strconv.Itoa(s))
		}
		for _, m := range metrics {
			ymax := axisMax(rep, m)
			label := strings.ToLower(m.Name) + " (" + m.Unit + ")"
			for _, op := range rep.Operations() {
				var vals []string
				for _, s := range rep.Scales() {
					c, ok := rep.cell("", op, s)
					stat := m.Value(c)
					if !ok || stat.N == 0 {
						continue
					}
					vals = append(vals, trim(stat.Median))
				}
				chart(b, fmt.Sprintf("%s — %s", op, label), "files in tree", label, xs, vals, ymax, "line")
			}
		}
	case KindComparison:
		ymax := axisMax(rep, Seconds)
		tools := rep.Tools()
		scale := rep.Scales()[0]
		for _, op := range rep.Operations() {
			var xs, vals []string
			for _, t := range tools {
				c, ok := rep.cell(t, op, scale)
				if !ok {
					continue
				}
				xs = append(xs, t)
				vals = append(vals, trim(c.Seconds.Median))
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
//
// It bounds the medians, which are what get plotted — bounding the raw samples
// would size every axis for an outlier that appears on no chart.
func axisMax(rep *Report, m Metric) float64 {
	var max float64
	for _, t := range rep.Tools() {
		for _, op := range rep.Operations() {
			for _, s := range rep.Scales() {
				c, ok := rep.cell(t, op, s)
				if !ok {
					continue
				}
				if v := m.Value(c).Median; v > max {
					max = v
				}
			}
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
