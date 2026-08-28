package main

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

// A single-size MinIO run: one tool, requests and bytes populated. "check"
// carries a genuine zero request count — an already-verified repository can
// legitimately issue no calls — which the report must still show as 0, not as
// an unmeasured dash.
const minioComparisonCSV = `tool,operation,scale,seconds,peak_mb,repo_delta,requests,sent_mb,by_api
cloudstic,backup,5000,1.10,204.2,24.6 MB,25,0,GetObject=8;HeadObject=3;ListObjectsV2=8;PutObject=5
cloudstic,check,5000,3.50,276.7,0 KB,0,0,
`

// The same measurements, but as a scaling sweep across tree sizes.
const minioScalingCSV = `tool,operation,scale,seconds,peak_mb,repo_delta,requests,sent_mb,by_api
cloudstic,backup,5000,1.10,204.2,24.6 MB,25,0,GetObject=8;HeadObject=3;PutObject=5
cloudstic,backup,20000,4.32,366.9,100.0 MB,60,0.9,GetObject=17;HeadObject=3;PutObject=13
cloudstic,restore,5000,11.20,537.2,0 KB,151,505.8,GetObject=143;HeadObject=3;ListObjectsV2=3
cloudstic,restore,20000,20.46,755.9,0 KB,280,1200.4,GetObject=260;HeadObject=3;ListObjectsV2=3
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

// Wall time is the headline a scaling sweep exists to produce, and it was
// measured all along — but only the comparison renderer used to print it, so a
// multi-size run wrote its timings to the CSV and showed none of them in the
// summary. The chart matters as much as the table: without it, time is the one
// metric a reader cannot see the shape of.
func TestScalingReportShowsWallTime(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, scalingCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, "### Wall time (s)") {
		t.Errorf("wall time table missing; got:\n%s", out)
	}
	// backup-initial's timings, which appear nowhere else in this report.
	for _, want := range []string{"| 0.5 |", "| 1.2 |", "| 5.49 |"} {
		if !strings.Contains(out, want) {
			t.Errorf("wall time cell %q missing; got:\n%s", want, out)
		}
	}
	// 5.49 / 0.50 = 10.98
	if !strings.Contains(out, "10.98x") {
		t.Errorf("wall time growth column missing or wrong; got:\n%s", out)
	}
	if !strings.Contains(out, `title "backup-initial — time (s)"`) {
		t.Errorf("wall time chart missing; got:\n%s", out)
	}

	// Time leads; memory follows. A reader scanning the summary should meet
	// the headline before the diagnostics.
	if secs, peak := strings.Index(out, "### Wall time"), strings.Index(out, "### Peak RSS"); secs > peak {
		t.Errorf("peak RSS precedes wall time; got:\n%s", out)
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

// ---------------------------------------------------------------------------
// S3 columns (requests, bytes, per-API breakdown)
// ---------------------------------------------------------------------------

// Parse must read the three S3 columns and set HasS3 from their presence,
// not from whether the values happen to be non-zero.
func TestParseReadsS3Columns(t *testing.T) {
	rep := parse(t, minioComparisonCSV)
	c, ok := rep.cell("cloudstic", "backup", 5000)
	if !ok {
		t.Fatal("cell not found")
	}
	if !c.HasS3 {
		t.Fatal("HasS3 should be true for a row with a requests column")
	}
	if c.Requests.Median != 25 {
		t.Errorf("requests = %v, want 25", c.Requests.Median)
	}
	if c.ByAPI != "GetObject=8;HeadObject=3;ListObjectsV2=8;PutObject=5" {
		t.Errorf("by_api = %q", c.ByAPI)
	}
}

// A zero request count is a real measurement — an already-verified repository
// can legitimately issue no calls to check itself — and must render as 0, not
// as the dash used for a backend that was never measured at all.
func TestZeroRequestsIsARealMeasurementNotADash(t *testing.T) {
	rep := parse(t, minioComparisonCSV)
	c, ok := rep.cell("cloudstic", "check", 5000)
	if !ok {
		t.Fatal("cell not found")
	}
	if !c.HasS3 {
		t.Fatal("a row with requests=0 must still count as measured")
	}
	if c.Requests.Median != 0 {
		t.Errorf("requests = %v, want 0", c.Requests.Median)
	}

	var b strings.Builder
	if err := Render(&b, rep, "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	line := lineContaining(t, b.String(), "`check`")
	if !strings.Contains(line, "| 0 |") {
		t.Errorf("zero requests should render as 0, not a dash:\n%s", line)
	}
}

// A malformed requests value is an error, matching every other numeric column:
// a silent zero in a performance table is indistinguishable from a real zero.
func TestParseRejectsMalformedRequests(t *testing.T) {
	_, err := Parse(strings.NewReader(
		"tool,operation,scale,seconds,peak_mb,requests\ncloudstic,backup,5000,1.0,200.0,many\n"))
	if err == nil {
		t.Fatal("expected an error for a non-numeric requests value")
	}
	if !strings.Contains(err.Error(), "requests") {
		t.Errorf("error should name the offending column, got: %v", err)
	}
}

// A single-size MinIO run must show requests and bytes in the comparison
// table, gated the same way alloc and repo_delta are.
func TestComparisonTableShowsRequestsAndSent(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, minioComparisonCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "Requests |") || !strings.Contains(out, "Sent |") {
		t.Errorf("requests/sent columns missing; got:\n%s", out)
	}
	line := lineContaining(t, out, "`backup`")
	if !strings.Contains(line, "| 25 |") {
		t.Errorf("request count missing from backup row; got:\n%s", line)
	}
}

// A local-only run — no requests/sent_mb/by_api columns at all — must render
// exactly as it did before S3 columns existed: no empty Requests/Sent columns,
// no empty by-API block.
func TestComparisonTableOmitsRequestsWhenUnmeasured(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, comparisonCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if strings.Contains(out, "Requests") || strings.Contains(out, "Sent |") {
		t.Errorf("requests/sent columns rendered for a run that measured neither:\n%s", out)
	}
	if strings.Contains(out, "Requests by API") {
		t.Errorf("by-API block rendered with nothing to show:\n%s", out)
	}
}

// A scaling sweep against MinIO gets its own Requests and Sent tables,
// alongside Peak RSS — the same treatment AllocMB gets.
func TestScalingReportShowsRequestsAndSentTables(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, minioScalingCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "### Requests") {
		t.Errorf("requests table missing; got:\n%s", out)
	}
	if !strings.Contains(out, "### Sent (MB)") {
		t.Errorf("sent table missing; got:\n%s", out)
	}
	// backup: 60 requests at 20000 files, 25 at 5000 -> 2.40x.
	if !strings.Contains(out, "2.40x") {
		t.Errorf("requests growth column missing or wrong; got:\n%s", out)
	}
}

// The per-API breakdown is long, so it belongs in a collapsed block rather
// than a column — but it still has to reach the page.
func TestByAPIRendersInACollapsedBlock(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, parse(t, minioComparisonCSV), "T"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "<details>") || !strings.Contains(out, "Requests by API") {
		t.Errorf("by-API details block missing; got:\n%s", out)
	}
	if !strings.Contains(out, "GetObject=8;HeadObject=3;ListObjectsV2=8;PutObject=5") {
		t.Errorf("by-API breakdown missing; got:\n%s", out)
	}
}

// A run with no S3 data must not grow an empty by-API block — the memory
// sweep and every cross-tool comparison land here.
func TestByAPIOmittedWhenNoS3Data(t *testing.T) {
	for name, csv := range map[string]string{"scaling": scalingCSV, "comparison": comparisonCSV} {
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			if err := Render(&b, parse(t, csv), "T"); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if strings.Contains(b.String(), "Requests by API") {
				t.Errorf("by-API block rendered with nothing to show:\n%s", b.String())
			}
		})
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

// Aging rows describe a different axis than the pipeline rows they share a CSV
// with, so they must not appear in the per-operation tables — a "restore@25"
// row there would be read as another operation measured at the sweep's tree
// sizes, which is not what it is.
func TestAgingRowsAreSeparatedFromPipelineRows(t *testing.T) {
	csv := `tool,backend,profile,scale,sample,operation,seconds,peak_mb,alloc_mb,requests,sent_mb,by_api,repo_delta,packs,backups,policy
cloudstic,minio,source,5000,1,backup,1.10,150.0,200.0,25,0.0,,1 MB,,,
cloudstic,minio,source,5000,1,restore,4.00,300.0,900.0,900,300.0,,0 KB,,,
cloudstic,minio,source,5000,1,restore@1,0.40,150.0,60.0,23,8.1,,0 KB,1,1,baseline
cloudstic,minio,source,5000,1,restore@25,0.55,158.0,70.0,199,20.4,,0 KB,25,25,baseline
cloudstic,minio,source,5000,1,check@1,0.20,150.0,60.0,16,8.1,,0 KB,1,1,baseline
cloudstic,minio,source,5000,1,check@25,0.30,157.0,70.0,210,20.5,,0 KB,25,25,baseline
`
	rep, err := Parse(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, op := range rep.Operations() {
		if strings.Contains(op, "@") {
			t.Errorf("aging operation %q leaked into the pipeline operations", op)
		}
	}
	if got := len(rep.Operations()); got != 2 {
		t.Errorf("pipeline operations = %d, want 2 (backup, restore)", got)
	}
	if got := len(rep.AgingRows()); got != 4 {
		t.Errorf("aging rows = %d, want 4", got)
	}

	var b strings.Builder
	if err := Render(&b, rep, "test"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := b.String()

	for _, want := range []string{
		"### Aging",
		"**restore**",
		"**check**",
		"| Backups | Packs |",                // packs column, since these rows report packs
		"1 → 25 backups: **8.65x requests**", // 199/23, the growth the table exists to show
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report is missing %q", want)
		}
	}

	// The aging curve must be ordered by backup count, not by CSV order.
	first := strings.Index(out, "| 1 | 1 | 23 |")
	second := strings.Index(out, "| 25 | 25 | 199 |")
	if first < 0 || second < 0 || first > second {
		t.Errorf("aging rows are not ordered by backup count (positions %d, %d)", first, second)
	}
}

// A report with no aging rows must not grow an empty section.
func TestNoAgingSectionWithoutAgingRows(t *testing.T) {
	csv := `tool,operation,scale,seconds,peak_mb
cloudstic,backup,5000,1.10,150.0
cloudstic,restore,5000,4.00,300.0
`
	rep, err := Parse(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var b strings.Builder
	if err := Render(&b, rep, "test"); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(b.String(), "### Aging") {
		t.Error("report without aging rows still rendered an Aging section")
	}
}

// The retention table is the only place the aging stage's writes are visible.
// The aging backups are setup rather than measurements, so nothing they store
// appears in a repo_delta, and a delta taken around a read is zero however much
// history the repository is carrying (issue #525).
func TestAgingReportsWhatARetainedSnapshotCosts(t *testing.T) {
	csv := `tool,backend,profile,scale,sample,operation,seconds,peak_mb,alloc_mb,requests,sent_mb,by_api,repo_delta,packs,backups,policy,stored_kb
cloudstic,local,source,2000,1,restore@1,0.40,150.0,60.0,,,,0 KB,,1,baseline,6144
cloudstic,local,source,2000,1,restore@3,0.42,151.0,61.0,,,,0 KB,,3,baseline,17408
cloudstic,local,source,2000,1,restore@5,0.44,152.0,62.0,,,,0 KB,,5,baseline,28672
`
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "test"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := b.String()

	for _, want := range []string{
		"**Retained size**",
		"| 1 | 6.0 | — |",    // the first checkpoint has no interval to average over
		"| 3 | 17.0 | 5.5 |", // (17.0 - 6.0) / 2 backups
		"| 5 | 28.0 | 5.5 |",
		"1 → 5 backups: **5.5 MB per retained snapshot**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report is missing %q\n%s", want, out)
		}
	}
}

// AGE_FINAL_OPS runs `backup` and `prune` after the last checkpoint under that
// checkpoint's backup count. A prune's total is the repository with its history
// collected, which is the opposite of what the retention table reports, so the
// first row at each checkpoint is the one that counts.
func TestRetainedSizeIgnoresPostCheckpointMutations(t *testing.T) {
	csv := `tool,backend,profile,scale,sample,operation,seconds,peak_mb,alloc_mb,requests,sent_mb,by_api,repo_delta,packs,backups,policy,stored_kb
cloudstic,local,source,2000,1,restore@1,0.40,150.0,60.0,,,,0 KB,,1,baseline,6144
cloudstic,local,source,2000,1,restore@5,0.44,152.0,62.0,,,,0 KB,,5,baseline,28672
cloudstic,local,source,2000,1,prune@5,2.10,200.0,80.0,,,,-22 MB,,5,baseline,6400
`
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "test"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, "| 5 | 28.0 | 5.5 |") {
		t.Errorf("retained size at 5 backups was taken from the prune row, not the checkpoint read\n%s", out)
	}
	if strings.Contains(out, "| 5 | 6.2 |") {
		t.Error("the pruned repository size was reported as the retained size")
	}
}

// A run without the stored_kb column — an older CSV, or compare.sh — must not
// grow an empty table.
func TestNoRetentionTableWithoutStoredSizes(t *testing.T) {
	csv := `tool,backend,profile,scale,sample,operation,seconds,peak_mb,alloc_mb,requests,sent_mb,by_api,repo_delta,packs,backups,policy
cloudstic,minio,source,5000,1,restore@1,0.40,150.0,60.0,23,8.1,,0 KB,1,1,baseline
cloudstic,minio,source,5000,1,restore@25,0.55,158.0,70.0,199,20.4,,0 KB,25,25,baseline
`
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "test"); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(b.String(), "**Retained size**") {
		t.Error("a report with no stored sizes still rendered a retention table")
	}
}

// Policies are interleaved at every checkpoint, so their series normally match
// — but a failed measurement writes no row, and lining the columns up by
// position would then render a policy's later values against a backup count
// they were not taken at. A gap has to stay a gap.
func TestRetainedSizeMatchesEachPolicyByBackupCount(t *testing.T) {
	csv := `tool,backend,profile,scale,sample,operation,seconds,peak_mb,alloc_mb,requests,sent_mb,by_api,repo_delta,packs,backups,policy,stored_kb
cloudstic,local,source,2000,1,restore@1,0.40,150.0,60.0,,,,0 KB,,1,baseline,6144
cloudstic,local,source,2000,1,restore@1:probe,0.40,150.0,60.0,,,,0 KB,,1,probe,5120
cloudstic,local,source,2000,1,restore@3,0.42,151.0,61.0,,,,0 KB,,3,baseline,17408
cloudstic,local,source,2000,1,restore@5,0.44,152.0,62.0,,,,0 KB,,5,baseline,28672
cloudstic,local,source,2000,1,restore@5:probe,0.44,152.0,62.0,,,,0 KB,,5,probe,20480
`
	var b strings.Builder
	if err := Render(&b, parse(t, csv), "test"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := b.String()

	for _, want := range []string{
		"| Backups | baseline (MB) | per backup | probe (MB) | per backup |",
		"| 3 | 17.0 | 5.5 | — | — |",
		// probe's marginal spans the interval it actually measured across —
		// four backups, not the two the row above it covers.
		"| 5 | 28.0 | 5.5 | 20.0 | 3.8 |",
		"1 → 5 backups (probe): **3.8 MB per retained snapshot**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report is missing %q\n%s", want, out)
		}
	}
}
