package benchreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This logic used to be awk and bc inside a shell script, where it could not be
// tested at all. These are the cases that were previously only checked by
// looking at the rendered output and hoping.

const scalingCSV = `tool,operation,scale,seconds,peak_mb,repo_delta
cloudstic,backup-initial,5000,0.50,148.9,
cloudstic,backup-initial,20000,1.20,188.1,
cloudstic,backup-initial,50000,5.49,348.5,
cloudstic,diff,5000,0.10,129.1,
cloudstic,diff,20000,0.40,154.6,
cloudstic,diff,50000,1.50,220.1,
`

const comparisonCSV = `tool,operation,scale,seconds,peak_mb,repo_delta
cloudstic,Initial Backup,0,12.30,450.2,1.2 GB
restic,Initial Backup,0,15.10,380.0,1.3 GB
borg,Initial Backup,0,22.40,210.5,900 MB
cloudstic,Full Restore,0,8.10,300.1,-
restic,Full Restore,0,9.90,280.4,-
borg,Full Restore,0,14.20,190.2,-
`

func parse(t *testing.T, csv string) *Report {
	t.Helper()
	rep, err := Parse(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return rep
}

// Which chart to draw is read off the data rather than passed in, so the two
// harnesses need no flag to tell them apart.
func TestKindIsDerivedFromWhichColumnVaries(t *testing.T) {
	if got := parse(t, scalingCSV).Kind(); got != KindScaling {
		t.Errorf("several sizes for one tool should be a scaling report, got %v", got)
	}
	if got := parse(t, comparisonCSV).Kind(); got != KindComparison {
		t.Errorf("several tools at one size should be a comparison report, got %v", got)
	}
}

// Operations must keep the order the harness ran them in — a reader compares
// the report against the console output line by line.
func TestOperationsKeepFirstSeenOrder(t *testing.T) {
	got := parse(t, comparisonCSV).Operations()
	want := []string{"Initial Backup", "Full Restore"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("operation %d = %q, want %q (order must follow the run)", i, got[i], want[i])
		}
	}
}

func TestScalingTableReportsGrowth(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, scalingCSV), "Memory"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()

	// 348.5 / 148.9 = 2.34
	if !strings.Contains(out, "2.34x") {
		t.Errorf("growth column missing or wrong; got:\n%s", out)
	}
	if !strings.Contains(out, "| 50,000 files |") {
		t.Errorf("size headings should group thousands; got:\n%s", out)
	}
}

// The table has to precede the charts and survive on its own: a renderer that
// does not know xychart-beta shows the chart as source text, and the numbers
// still have to be readable.
func TestTablePrecedesCharts(t *testing.T) {
	for name, csv := range map[string]string{"scaling": scalingCSV, "comparison": comparisonCSV} {
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			if err := Render(&b, parse(t, csv), "T"); err != nil {
				t.Fatalf("Render: %v", err)
			}
			out := b.String()
			table, chart := strings.Index(out, "| Operation |"), strings.Index(out, "```mermaid")
			if table < 0 {
				t.Fatal("no table rendered")
			}
			if chart < 0 {
				t.Fatal("no chart rendered")
			}
			if table > chart {
				t.Error("charts precede the table; a renderer without xychart support would " +
					"then lead with a block of source text")
			}
		})
	}
}

// Mermaid needs quotes around a category containing a space or dash, and tool
// names have both.
func TestComparisonChartQuotesToolNames(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, comparisonCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(b.String(), `["cloudstic", "restic", "borg"]`) {
		t.Errorf("tool names must be quoted on the x-axis; got:\n%s", b.String())
	}
}

// A numeric axis must not be quoted, or mermaid treats the points as
// categories and spaces them evenly regardless of value.
func TestScalingChartLeavesNumbersUnquoted(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, scalingCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(b.String(), "[5000, 20000, 50000]") {
		t.Errorf("numeric x-axis should be unquoted; got:\n%s", b.String())
	}
}

// A tool that does not implement an operation leaves a hole. It must render as
// a dash, never as a zero, which would read as "took no time".
func TestMissingCellRendersAsDashNotZero(t *testing.T) {
	csv := comparisonCSV + "duplicacy,Initial Backup,0,30.00,500.0,1.4 GB\n"
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	// duplicacy has no Full Restore row.
	line := lineContaining(t, out, "`Full Restore`")
	if !strings.Contains(line, "- |") {
		t.Errorf("unmeasured cell should be a dash; got row:\n%s", line)
	}
	if strings.Contains(line, "0s") {
		t.Errorf("unmeasured cell rendered as a zero measurement:\n%s", line)
	}
}

// A malformed number is an error, not a zero: a silent zero in a performance
// table is indistinguishable from a real result.
func TestParseRejectsMalformedNumbers(t *testing.T) {
	_, err := Parse(strings.NewReader(
		"tool,operation,scale,seconds,peak_mb,repo_delta\ncloudstic,backup,5000,fast,148.9,\n"))
	if err == nil {
		t.Fatal("expected an error for a non-numeric seconds value")
	}
	if !strings.Contains(err.Error(), "seconds") {
		t.Errorf("error should name the offending column, got: %v", err)
	}
}

func TestParseRejectsCSVWithoutOperation(t *testing.T) {
	_, err := Parse(strings.NewReader("tool,scale\ncloudstic,5000\n"))
	if err == nil || !strings.Contains(err.Error(), "operation") {
		t.Fatalf("expected a missing-column error naming 'operation', got: %v", err)
	}
}

// Columns are found by header name, so a harness may emit a subset in its own
// order and gain columns later without breaking the reader.
func TestParseIsIndifferentToColumnOrder(t *testing.T) {
	rep, err := Parse(strings.NewReader(
		"operation,peak_mb,scale\nbackup,148.9,5000\nbackup,188.1,20000\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(rep.Rows) != 2 || rep.Rows[0].PeakMB != 148.9 || rep.Rows[0].Scale != 5000 {
		t.Fatalf("columns misread: %+v", rep.Rows)
	}
	if rep.Rows[0].Tool != "cloudstic" {
		t.Errorf("Tool should default to cloudstic when the column is absent, got %q", rep.Rows[0].Tool)
	}
}

// AppendRow is what makes an interrupted sweep still worth something.
func TestAppendRowWritesHeaderOnceThenAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.csv")
	for i, op := range []string{"backup", "restore"} {
		if err := AppendRow(path, Row{
			Tool: "cloudstic", Operation: op, Scale: 100 * (i + 1), Seconds: 1.5, PeakMB: 42,
		}); err != nil {
			t.Fatalf("AppendRow: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines:\n%s", len(lines), data)
	}
	if strings.Count(string(data), "operation") != 1 {
		t.Errorf("header written more than once:\n%s", data)
	}
	if _, err := Parse(strings.NewReader(string(data))); err != nil {
		t.Errorf("AppendRow produced something Parse cannot read: %v", err)
	}
}

// Scale is omitted rather than written as 0 when a harness has one size, so a
// comparison report is not mistaken for a one-point scaling sweep.
func TestAppendRowOmitsZeroScale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.csv")
	if err := AppendRow(path, Row{Tool: "restic", Operation: "backup", Seconds: 2, PeakMB: 10}); err != nil {
		t.Fatalf("AppendRow: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), ",0,") {
		t.Errorf("zero scale should be written empty, got:\n%s", data)
	}
}

// A single-tool run is a self-benchmark, not a comparison, and its prose has to
// say so — "each tool over the same dataset" reads as a missing column.
func TestSingleToolRunIsNotDescribedAsAComparison(t *testing.T) {
	csv := "tool,operation,scale,seconds,peak_mb,repo_delta\ncloudstic,Initial Backup,,0.70,534.4,344.0 MB\n"
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(b.String(), "Each tool") {
		t.Errorf("one tool described as a cross-tool comparison:\n%s", b.String())
	}
}

// Repository growth is the column that shows deduplication working, so a
// self-benchmark must surface it.
func TestSingleToolTableShowsRepoGrowth(t *testing.T) {
	csv := "tool,operation,scale,seconds,peak_mb,repo_delta\n" +
		"cloudstic,Deduplicated Backup,,0.54,343.4,40 KB\n"
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "Repo added") || !strings.Contains(out, "40 KB") {
		t.Errorf("repository growth missing from a single-tool table:\n%s", out)
	}
}

// Across tools the same column would invite a comparison it cannot support:
// each writes a different format, so "bytes added" is not the same measurement
// twice.
func TestComparisonTableOmitsRepoGrowth(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, comparisonCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(b.String(), "Repo added") {
		t.Errorf("repository growth shown across tools, which compares different formats:\n%s", b.String())
	}
}

// The memory sweep records no repository growth; its table must not grow an
// empty column because of it.
func TestScalingTableHasNoRepoColumn(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, scalingCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(b.String(), "Repo added") {
		t.Errorf("scaling table grew a column it has no data for:\n%s", b.String())
	}
}

// ---------------------------------------------------------------------------
// Repository summary rows
// ---------------------------------------------------------------------------

// Final Repo Size and Logical / Stored are not timed operations. They must not
// show up in the per-operation tables and charts as a row of zeros.
func TestSummaryRowsExcludedFromOperations(t *testing.T) {
	csv := "tool,operation,scale,seconds,peak_mb,repo_delta\n" +
		"cloudstic,Initial Backup,,0.70,534.4,344.0 MB\n" +
		"cloudstic,Final Repo Size,,0,0,546.0 MB\n" +
		"cloudstic,Logical / Stored,,0,0,1.16 GB / 545.7 MB (2.18x)\n"
	ops := parse(t, csv).Operations()
	if len(ops) != 1 || ops[0] != "Initial Backup" {
		t.Errorf("Operations() = %v, want only [Initial Backup]", ops)
	}
}

// The two summary rows get their own section rather than a table row, and the
// values must reach the page.
func TestRepoSummaryRendersFinalSizeAndRatio(t *testing.T) {
	csv := "tool,operation,scale,seconds,peak_mb,repo_delta\n" +
		"cloudstic,Initial Backup,,0.70,534.4,344.0 MB\n" +
		"cloudstic,Final Repo Size,,0,0,546.0 MB\n" +
		"cloudstic,Logical / Stored,,0,0,1.16 GB / 545.7 MB (2.18x)\n"
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "### Repository") {
		t.Fatalf("no repository summary section; got:\n%s", out)
	}
	if !strings.Contains(out, "546.0 MB") {
		t.Errorf("final repo size missing; got:\n%s", out)
	}
	if !strings.Contains(out, "1.16 GB / 545.7 MB (2.18x)") {
		t.Errorf("logical/stored ratio missing; got:\n%s", out)
	}
}

// A scaling sweep gets one row of the summary per tree size, not just the
// last one measured.
func TestRepoSummaryRendersPerScaleForAScalingSweep(t *testing.T) {
	csv := "tool,operation,scale,seconds,peak_mb,repo_delta\n" +
		"cloudstic,backup,5000,0.50,148.9,\n" +
		"cloudstic,backup,20000,1.20,188.1,\n" +
		"cloudstic,Final Repo Size,5000,0,0,50.0 MB\n" +
		"cloudstic,Final Repo Size,20000,0,0,200.0 MB\n"
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "50.0 MB") || !strings.Contains(out, "200.0 MB") {
		t.Errorf("expected a final repo size row per scale; got:\n%s", out)
	}
}

// A harness that never measured the repository (the memory sweep) must not
// grow an empty summary section.
func TestNoRepoSummaryWhenUnmeasured(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, scalingCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(b.String(), "### Repository") {
		t.Errorf("repository summary rendered with nothing measured:\n%s", b.String())
	}
}

// ---------------------------------------------------------------------------
// Repeated samples
// ---------------------------------------------------------------------------

// Three runs of the same point, deliberately noisy: 408/405/352 is the spread
// actually observed on one machine while chasing a 36 MB regression, which a
// single sample could not resolve at all.
const sampledCSV = `tool,operation,scale,seconds,peak_mb,alloc_mb,repo_delta
cloudstic,prune,5000,1.00,408.0,900.0,
cloudstic,prune,5000,1.10,405.0,901.0,
cloudstic,prune,5000,0.90,352.0,900.5,
cloudstic,prune,20000,2.00,512.0,1800.0,
cloudstic,prune,20000,2.10,515.0,1802.0,
cloudstic,prune,20000,1.90,511.0,1801.0,
`

// The whole point of sampling: one wild run must not become the reported
// number. The mean of 408/405/352 is 388.3 — reporting that would fold the
// outlier into the result instead of discarding it.
func TestRepeatedSamplesReportTheMedian(t *testing.T) {
	rep := parse(t, sampledCSV)
	c, ok := rep.cell("", "prune", 5000)
	if !ok {
		t.Fatal("point not found")
	}
	if c.PeakMB.Median != 405 {
		t.Errorf("median peak RSS = %v, want 405 (not the mean 388.3, not the first sample 408)", c.PeakMB.Median)
	}
	if c.PeakMB.Min != 352 || c.PeakMB.Max != 408 {
		t.Errorf("band = %v–%v, want 352–408", c.PeakMB.Min, c.PeakMB.Max)
	}
	if c.PeakMB.N != 3 {
		t.Errorf("N = %d, want 3", c.PeakMB.N)
	}
}

// A median on its own is a confident-looking number with no error bar. The
// band has to reach the page, or a 5 MB difference inside a 56 MB spread reads
// as a result.
func TestScalingTableShowsTheSampleBand(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, sampledCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "405 (352–408)") {
		t.Errorf("median with min–max band missing; got:\n%s", out)
	}
	if !strings.Contains(out, "median of 3 runs") {
		t.Errorf("prose should say how many runs the median came from; got:\n%s", out)
	}
}

// A harness taking one sample per point must not grow a band of one value.
func TestSingleSampleRendersBareValue(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, scalingCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if strings.Contains(out, "(148.9–148.9)") {
		t.Errorf("single sample rendered as a degenerate band:\n%s", out)
	}
	if strings.Contains(out, "median of") {
		t.Errorf("unsampled report claims to report a median:\n%s", out)
	}
}

func TestMedianOfEvenSampleCountAveragesTheMiddle(t *testing.T) {
	got := newStat([]float64{10, 20, 30, 40}).Median
	if got != 25 {
		t.Errorf("median of 4 samples = %v, want 25", got)
	}
}

// ---------------------------------------------------------------------------
// Allocation totals
// ---------------------------------------------------------------------------

// The column that exists because peak RSS could not see PR #449: a change that
// removed 36 MB of transient allocation moved the high-water mark by less than
// the run-to-run noise.
func TestAllocationTotalGetsItsOwnTable(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, sampledCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "### Total allocated (MB)") {
		t.Errorf("allocation table missing; got:\n%s", out)
	}
	if !strings.Contains(out, "### Peak RSS (MB)") {
		t.Errorf("peak RSS table missing; got:\n%s", out)
	}
	// 900.5 is the median of 900.0/901.0/900.5.
	if !strings.Contains(out, "900.5") {
		t.Errorf("allocation median missing; got:\n%s", out)
	}
}

// A harness that cannot report allocation — every tool in compare.sh except
// cloudstic — must not produce an empty table.
func TestReportWithoutAllocationHasNoAllocationTable(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, scalingCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(b.String(), "Total allocated") {
		t.Errorf("allocation table rendered for a sweep that measured none:\n%s", b.String())
	}
}

// An unmeasured sample is absent, not zero. Averaging a real 900 MB with a
// placeholder 0 would report 450 MB, which is a plausible-looking lie.
func TestUnmeasuredAllocationIsExcludedNotAveragedAsZero(t *testing.T) {
	csv := "tool,operation,scale,seconds,peak_mb,alloc_mb\n" +
		"cloudstic,prune,5000,1.00,400.0,900.0\n" +
		"cloudstic,prune,5000,1.00,400.0,\n"
	rep := parse(t, csv)
	c, ok := rep.cell("", "prune", 5000)
	if !ok {
		t.Fatal("point not found")
	}
	if c.AllocMB.N != 1 || c.AllocMB.Median != 900 {
		t.Errorf("alloc = %+v, want a single 900 sample with the blank excluded", c.AllocMB)
	}
	if c.PeakMB.N != 2 {
		t.Errorf("peak RSS should still count both samples, got N=%d", c.PeakMB.N)
	}
}

// The CSV keeps one row per sample so the artifact shows the spread; a reader
// (and a later re-render) has to be able to get back to the same numbers.
func TestAppendRowRoundTripsAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.csv")
	if err := AppendRow(path, Row{
		Tool: "cloudstic", Operation: "prune", Scale: 5000, Seconds: 1, PeakMB: 400, AllocMB: 936.5,
	}); err != nil {
		t.Fatalf("AppendRow: %v", err)
	}
	// A tool with no allocation number leaves the column empty rather than 0.
	if err := AppendRow(path, Row{Tool: "restic", Operation: "prune", Scale: 5000, Seconds: 1, PeakMB: 380}); err != nil {
		t.Fatalf("AppendRow: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Parse(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if rep.Rows[0].AllocMB != 936.5 {
		t.Errorf("alloc_mb did not round-trip: %v", rep.Rows[0].AllocMB)
	}
	if rep.Rows[1].AllocMB != 0 {
		t.Errorf("unmeasured alloc should read back as 0, got %v", rep.Rows[1].AllocMB)
	}
	if strings.Contains(string(data), ",0.0,") {
		t.Errorf("unmeasured allocation written as a zero measurement:\n%s", data)
	}
}

// A -memstats file that is missing or empty means the flag never reached the
// measured command. Recording 0 would put a hole in the column that reads as
// "this operation allocates nothing".
func TestReadAllocMBRejectsAMissingOrEmptyProfile(t *testing.T) {
	if _, err := ReadAllocMB(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("expected an error for a missing memstats file")
	}

	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{"total_alloc_bytes":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAllocMB(path); err == nil {
		t.Error("expected an error for a memstats file reporting no allocation")
	}
}

func TestReadAllocMBConvertsToMegabytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ms.json")
	if err := os.WriteFile(path, []byte(`{"total_alloc_bytes":36700160}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAllocMB(path)
	if err != nil {
		t.Fatalf("ReadAllocMB: %v", err)
	}
	if got != 35 {
		t.Errorf("got %v MB, want 35", got)
	}
}

func lineContaining(t *testing.T, s, want string) string {
	t.Helper()
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no line containing %q in:\n%s", want, s)
	return ""
}
