package main

import (
	"fmt"
	"io"
	"sort"
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
		b.WriteString("Measurements of the real binary, per operation, at each tree size.\n\n")
		writeSampleNote(b, rep)

		// Wall time leads because it is the headline this sweep exists to
		// produce. It was measured all along — bench.sh parses it from time(1)
		// and writes it to the CSV — but for a while only the comparison
		// renderer printed it, so a scaling run uploaded its timings in the
		// artifact and showed none of them in the summary.
		b.WriteString("### Wall time (s)\n\n")
		b.WriteString("How long each operation took. A shared runner carries several percent of\n" +
			"noise and its hardware differs from the next runner's, so read a column\n" +
			"against the others in the same run rather than against another run's.\n\n")
		renderScalingTable(b, rep, Seconds)

		b.WriteString("\n### Peak RSS (MB)\n\n")
		b.WriteString("The largest amount live at once — what decides whether an operation fits\n" +
			"in the memory available. A row that stays flat holds a working set; one that\n" +
			"tracks the file count holds a per-entry structure, and will eventually meet a\n" +
			"repository it cannot open.\n\n")
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
	renderAging(b, rep)
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
		// Same order as the tables above, so a reader scrolling from one to the
		// other meets the metrics in the same sequence.
		metrics := []Metric{Seconds, PeakMB}
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

// renderAging renders the aging stage: how an operation's cost grows with the
// number of backups that contributed to the snapshot it reads.
//
// It gets its own section because it is a different measurement over a
// different axis. The tables above vary tree size against a freshly created
// repository, which is the best case for any layout that amortises reads and
// says nothing about how a repository behaves once it has a history. This
// varies history with the tree held fixed, and it is the axis a linear
// per-backup term shows up on — the term that decides whether a year-old
// repository is still usable (RFC 0025 §4, RFC 0026).
//
// The rows are checkpoints and the columns are the metrics that carry that
// term: requests and bytes, because a per-backup cost is paid in round trips
// rather than in wall time on a loopback backend, plus peak RSS to show
// whether history is also costing memory. Packs is included where a format
// has them, since it is the mechanism behind the growth rather than a
// separate finding.
func renderAging(b *strings.Builder, rep *Report) {
	rows := rep.AgingRows()
	if len(rows) == 0 {
		return
	}

	// Group by (operation, policy) so each curve is one table, and collect the
	// checkpoints in ascending order.
	type curveKey struct{ op, policy string }
	curves := map[curveKey][]Row{}
	var order []curveKey
	for _, row := range rows {
		k := curveKey{op: AgingOp(row.Operation), policy: row.Policy}
		if _, seen := curves[k]; !seen {
			order = append(order, k)
		}
		curves[k] = append(curves[k], row)
	}

	b.WriteString("\n### Aging\n\n")
	b.WriteString("Cost against the number of backups the repository has taken, with the tree\n" +
		"held fixed. The tables above measure a freshly created repository, which is\n" +
		"the most favourable case for any layout that bundles objects and the least\n" +
		"representative one; this is the axis a per-backup cost appears on. A flat\n" +
		"column means reading a snapshot costs what its data costs. A rising one means\n" +
		"it costs what the repository's history costs.\n\n")

	renderRetention(b, rows)

	for _, k := range order {
		curve := curves[k]
		sort.Slice(curve, func(i, j int) bool { return curve[i].Backups < curve[j].Backups })

		heading := k.op
		if k.policy != "" && k.policy != "baseline" {
			heading = fmt.Sprintf("%s (%s)", k.op, k.policy)
		}
		fmt.Fprintf(b, "**%s**\n\n", heading)

		withPacks := false
		for _, row := range curve {
			if row.Packs > 0 {
				withPacks = true
				break
			}
		}

		b.WriteString("| Backups |")
		if withPacks {
			b.WriteString(" Packs |")
		}
		b.WriteString(" Requests | Sent (MB) | Peak RSS (MB) | Wall (s) |\n|---:|")
		if withPacks {
			b.WriteString("---:|")
		}
		b.WriteString("---:|---:|---:|---:|\n")

		for _, row := range curve {
			fmt.Fprintf(b, "| %d |", row.Backups)
			if withPacks {
				fmt.Fprintf(b, " %d |", row.Packs)
			}
			if row.HasS3 {
				fmt.Fprintf(b, " %d | %.1f |", row.Requests, row.SentMB)
			} else {
				b.WriteString(" — | — |")
			}
			fmt.Fprintf(b, " %.1f | %.2f |\n", row.PeakMB, row.Seconds)
		}

		// The growth factor is the point of the table, and reading it off two
		// rows is exactly the arithmetic a reader would otherwise do by hand.
		if len(curve) >= 2 {
			first, last := curve[0], curve[len(curve)-1]
			if first.HasS3 && first.Requests > 0 {
				fmt.Fprintf(b, "\n%d → %d backups: **%.2fx requests**, %.2fx bytes.\n\n",
					first.Backups, last.Backups,
					float64(last.Requests)/float64(first.Requests),
					ratioOrZero(last.SentMB, first.SentMB))
			} else {
				b.WriteString("\n")
			}
		}
	}
}

// renderRetention renders what retaining a snapshot costs, which is the one
// thing the aging stage measures that no per-operation table can show. The
// aging backups are setup rather than measurements, so their writes appear in
// no row's repo_delta, and a delta taken around a read is zero however much
// history the repository is carrying.
//
// The number that matters is the slope, not the total: a format that rewrites
// a whole leaf for one changed entry keeps a superseded copy of every leaf a
// backup touched, so each retained snapshot costs about the directories it
// touched times the leaf size — a figure independent of repository size, and
// invisible on a repository with one backup in it (RFC 0026, issue #525).
//
// Only the first row at each checkpoint is used. AGE_FINAL_OPS runs `backup`
// and `prune` after the last checkpoint under the same backup count, and a
// prune's total would otherwise read as that checkpoint's retained size.
func renderRetention(b *strings.Builder, rows []Row) {
	type point struct {
		backups int
		stored  float64
	}
	byPolicy := map[string][]point{}
	var order []string
	seen := map[string]bool{}
	for _, row := range rows {
		if row.StoredMB == 0 {
			continue
		}
		key := row.Policy + "\x00" + strconv.Itoa(row.Backups)
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := byPolicy[row.Policy]; !ok {
			order = append(order, row.Policy)
		}
		byPolicy[row.Policy] = append(byPolicy[row.Policy], point{row.Backups, row.StoredMB})
	}
	if len(order) == 0 {
		return
	}

	b.WriteString("**Retained size**\n\n")
	b.WriteString("| Backups |")
	for _, p := range order {
		// A policy name identifies a column only when there is something to
		// tell it apart from. A lone "baseline (MB)" names the default and
		// says nothing.
		name := "Stored"
		if len(order) > 1 {
			name = p
		}
		fmt.Fprintf(b, " %s (MB) | per backup |", name)
	}
	b.WriteString("\n|---:|")
	for range order {
		b.WriteString("---:|---:|")
	}
	b.WriteString("\n")

	// Checkpoints are shared across policies — they are measured against one
	// aged repository — so one row per checkpoint of the first policy covers
	// every column.
	for _, p := range order {
		sort.Slice(byPolicy[p], func(i, j int) bool { return byPolicy[p][i].backups < byPolicy[p][j].backups })
	}
	for i, ref := range byPolicy[order[0]] {
		fmt.Fprintf(b, "| %d |", ref.backups)
		for _, p := range order {
			pts := byPolicy[p]
			if i >= len(pts) {
				b.WriteString(" — | — |")
				continue
			}
			fmt.Fprintf(b, " %.1f |", pts[i].stored)
			// The marginal cost of the backups since the previous checkpoint,
			// which is the retained cost of one snapshot averaged over the
			// interval. The first checkpoint has no interval to average over.
			if i == 0 {
				b.WriteString(" — |")
				continue
			}
			prev := pts[i-1]
			if n := pts[i].backups - prev.backups; n > 0 {
				fmt.Fprintf(b, " %.1f |", (pts[i].stored-prev.stored)/float64(n))
			} else {
				b.WriteString(" — |")
			}
		}
		b.WriteString("\n")
	}

	first := byPolicy[order[0]]
	if len(first) >= 2 {
		last := first[len(first)-1]
		if n := last.backups - first[0].backups; n > 0 && first[0].stored > 0 {
			fmt.Fprintf(b, "\n%d → %d backups: **%.1f MB per retained snapshot**, %.2fx total.\n",
				first[0].backups, last.backups,
				(last.stored-first[0].stored)/float64(n), last.stored/first[0].stored)
		}
	}
	b.WriteString("\n")
}

func ratioOrZero(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
