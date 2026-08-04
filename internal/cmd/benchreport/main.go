// Command benchreport measures a benchmark step and renders the results.
//
// It is the analysis half of the benchmark harness. The scripts in
// scripts/benchmark still orchestrate — they build datasets and launch
// cloudstic, restic, borg and duplicacy, which is what shell is for — and call
// this for the two things shell does badly: measuring a child process, and
// turning a pile of numbers into a table.
//
//	benchreport run -op "Initial Backup" -out results.csv -- cloudstic backup …
//	benchreport render -in results.csv -title "Local benchmark" >> "$GITHUB_STEP_SUMMARY"
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/cloudstic/cli/internal/benchreport"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "row":
		err = rowCmd(os.Args[2:])
	case "render":
		err = renderCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchreport:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `benchreport — measure and report benchmark steps

  run     measure one command and append a CSV row
  row     append a CSV row already measured elsewhere
  render  turn a CSV into Markdown with charts

Run 'benchreport <subcommand> -h' for flags.
`)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	op := fs.String("op", "", "operation name (required)")
	tool := fs.String("tool", "cloudstic", "tool being measured")
	scale := fs.Int("scale", 0, "dataset size this step ran against")
	out := fs.String("out", "", "CSV to append to (required)")
	repoDelta := fs.String("repo-delta", "", "repository growth, pre-formatted")
	allocFrom := fs.String("alloc-from", "", "read the allocation total from this cloudstic -memstats file")
	quiet := fs.Bool("quiet", false, "discard the measured command's output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *op == "" || *out == "" {
		return fmt.Errorf("-op and -out are required")
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("no command given; put it after --")
	}

	stdout, stderr := os.Stdout, os.Stderr
	if *quiet {
		stdout, stderr = nil, nil
	}

	seconds, peakMB, err := benchreport.Measure(rest[0], rest[1:], stdout, stderr)
	if err != nil {
		return err
	}

	// The measured process writes this itself, so it is read after the run
	// rather than derived from anything the parent can observe. A missing or
	// unreadable file is fatal: silently recording no allocation total would
	// leave a hole in the column that reads as "this operation allocates
	// nothing".
	var allocMB float64
	if *allocFrom != "" {
		if allocMB, err = benchreport.ReadAllocMB(*allocFrom); err != nil {
			return err
		}
	}

	if err := benchreport.AppendRow(*out, benchreport.Row{
		Tool:      *tool,
		Operation: *op,
		Scale:     *scale,
		Seconds:   seconds,
		PeakMB:    peakMB,
		AllocMB:   allocMB,
		RepoDelta: *repoDelta,
	}); err != nil {
		return err
	}

	// Echo the row so a script's own console output stays readable without it
	// having to re-read the CSV it just appended to.
	alloc := ""
	if allocMB > 0 {
		alloc = fmt.Sprintf("  %8.1f MB alloc", allocMB)
	}
	fmt.Printf("  %-28s %10s  %8.1f MB peak  %7.2fs%s\n", *op, scaleLabel(*scale), peakMB, seconds, alloc)
	return nil
}

func scaleLabel(scale int) string {
	if scale == 0 {
		return ""
	}
	return fmt.Sprintf("%d files", scale)
}

// rowCmd exists for a harness that must do its own measuring.
//
// scripts/benchmark/run.sh is the case: it compares four tools, and a tool
// that is not installed has to leave a gap rather than abort the comparison —
// so it keeps its own timing and error handling, and hands the numbers here.
// Routing it through the same writer is what stops the two harnesses' CSV
// schemas from drifting apart.
func rowCmd(args []string) error {
	fs := flag.NewFlagSet("row", flag.ExitOnError)
	tool := fs.String("tool", "cloudstic", "tool measured")
	op := fs.String("op", "", "operation name (required)")
	scale := fs.Int("scale", 0, "dataset size this step ran against")
	seconds := fs.Float64("seconds", 0, "wall time")
	peakMB := fs.Float64("peak-mb", 0, "peak resident set size, MB")
	repoDelta := fs.String("repo-delta", "", "repository growth, pre-formatted")
	out := fs.String("out", "", "CSV to append to (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *op == "" || *out == "" {
		return fmt.Errorf("-op and -out are required")
	}
	return benchreport.AppendRow(*out, benchreport.Row{
		Tool:      *tool,
		Operation: *op,
		Scale:     *scale,
		Seconds:   *seconds,
		PeakMB:    *peakMB,
		RepoDelta: *repoDelta,
	})
}

func renderCmd(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	in := fs.String("in", "", "CSV to read (required)")
	title := fs.String("title", "Benchmark", "report heading")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("-in is required")
	}

	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	rep, err := benchreport.Parse(f)
	if err != nil {
		return err
	}
	return benchreport.Render(os.Stdout, rep, *title)
}
