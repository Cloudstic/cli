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
