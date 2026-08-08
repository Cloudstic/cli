package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Reserved operation names for the two summary rows a self-benchmark can emit:
// the repository's final size and its logical-to-stored ratio. Neither is a
// timed operation — they carry no seconds or peak_mb worth showing — so they
// are excluded from Operations() and rendered in their own section rather than
// polluting the per-operation time/memory tables with a row of zeros.
const (
	OpFinalRepoSize = "Final Repo Size"
	OpLogicalStored = "Logical / Stored"
)

func isSummaryOp(op string) bool {
	return op == OpFinalRepoSize || op == OpLogicalStored
}

// Row is one measured operation.
//
// One schema serves both harnesses because they differ only in which column
// varies. The memory sweep holds Tool constant and varies Scale; the
// competitive run holds Scale constant and varies Tool. Kind() reads that off
// the data rather than being told.
//
// A row is one *sample*, not one point. Several rows may share
// (Tool, Operation, Scale) — the memory sweep writes one per repetition, and
// the report reduces them with cell(). Keeping the samples in the CSV rather
// than collapsing them at write time means the artifact still shows how noisy a
// point was, which a median alone hides.
type Row struct {
	Tool      string // cloudstic, restic, borg, duplicacy
	Operation string // "backup-initial", "Initial Backup", …
	Scale     int    // files in tree; 0 when the harness has only one size
	Seconds   float64
	PeakMB    float64
	AllocMB   float64 // cumulative bytes allocated, including freed; 0 when unmeasured
	RepoDelta string  // human-formatted, passed through untouched
}

// Stat summarises repeated samples of one measurement.
//
// The median is reported rather than the mean because the noise here is
// one-sided: a run perturbed by another process on the runner is slow and
// memory-hungry, never the reverse, so a single bad sample drags a mean and
// leaves a median alone. Min and Max are carried alongside so a point whose
// samples disagree is visible as a wide band rather than hidden behind a
// confident-looking middle value.
type Stat struct {
	Median float64
	Min    float64
	Max    float64
	N      int
}

// Spread is the distance between the extremes — the width of the noise band a
// difference has to clear before it means anything.
func (s Stat) Spread() float64 { return s.Max - s.Min }

func newStat(vs []float64) Stat {
	if len(vs) == 0 {
		return Stat{}
	}
	sorted := append([]float64(nil), vs...)
	sort.Float64s(sorted)

	mid := len(sorted) / 2
	median := sorted[mid]
	if len(sorted)%2 == 0 {
		median = (sorted[mid-1] + sorted[mid]) / 2
	}
	return Stat{Median: median, Min: sorted[0], Max: sorted[len(sorted)-1], N: len(sorted)}
}

// Cell is every sample for one (tool, operation, scale) point, reduced.
type Cell struct {
	Seconds   Stat
	PeakMB    Stat
	AllocMB   Stat
	HasAlloc  bool
	RepoDelta string
}

// Report is a parsed measurement set.
type Report struct {
	Rows []Row
}

// Kind describes which column varies, and therefore which chart makes sense.
type Kind int

const (
	// KindScaling varies tree size for one tool: x-axis is the file count.
	KindScaling Kind = iota
	// KindComparison varies tool at one size: x-axis is the tool.
	KindComparison
)

// Parse reads the CSV emitted by the measurement scripts.
//
// The header names the columns, so a script may emit a subset in any order and
// gain columns later without breaking older reports.
func Parse(r io.Reader) (*Report, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // trailing empty columns are allowed

	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("csv has no data rows")
	}

	idx := map[string]int{}
	for i, name := range records[0] {
		idx[strings.TrimSpace(name)] = i
	}
	if _, ok := idx["operation"]; !ok {
		return nil, fmt.Errorf("csv is missing the 'operation' column")
	}

	get := func(rec []string, col string) string {
		i, ok := idx[col]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	rep := &Report{}
	for n, rec := range records[1:] {
		op := get(rec, "operation")
		if op == "" {
			continue // blank line between harness sections
		}
		row := Row{
			Tool:      get(rec, "tool"),
			Operation: op,
			RepoDelta: get(rec, "repo_delta"),
		}
		if row.Tool == "" {
			row.Tool = "cloudstic"
		}
		// A malformed number is reported rather than silently zeroed: a zero in
		// a performance table reads as a real measurement.
		if v := get(rec, "scale"); v != "" {
			if row.Scale, err = strconv.Atoi(v); err != nil {
				return nil, fmt.Errorf("row %d: scale %q: %w", n+2, v, err)
			}
		}
		if v := get(rec, "seconds"); v != "" {
			if row.Seconds, err = strconv.ParseFloat(v, 64); err != nil {
				return nil, fmt.Errorf("row %d: seconds %q: %w", n+2, v, err)
			}
		}
		if v := get(rec, "peak_mb"); v != "" {
			if row.PeakMB, err = strconv.ParseFloat(v, 64); err != nil {
				return nil, fmt.Errorf("row %d: peak_mb %q: %w", n+2, v, err)
			}
		}
		if v := get(rec, "alloc_mb"); v != "" {
			if row.AllocMB, err = strconv.ParseFloat(v, 64); err != nil {
				return nil, fmt.Errorf("row %d: alloc_mb %q: %w", n+2, v, err)
			}
		}
		rep.Rows = append(rep.Rows, row)
	}
	if len(rep.Rows) == 0 {
		return nil, fmt.Errorf("csv has no usable rows")
	}
	return rep, nil
}

// Kind reports whether this is a scaling sweep or a tool comparison.
func (r *Report) Kind() Kind {
	if len(r.Scales()) > 1 {
		return KindScaling
	}
	return KindComparison
}

// Operations returns the distinct operations in first-seen order, which is the
// order the harness ran them and therefore the order a reader expects. The two
// summary rows are excluded — they have their own section, rendered by
// renderRepoSummary.
func (r *Report) Operations() []string {
	all := distinct(r.Rows, func(row Row) string { return row.Operation })
	out := make([]string, 0, len(all))
	for _, op := range all {
		if !isSummaryOp(op) {
			out = append(out, op)
		}
	}
	return out
}

// hasSummaryRows reports whether the harness recorded a final repository size
// or a logical/stored ratio, which the memory sweep and compare.sh never do.
func (r *Report) hasSummaryRows() bool {
	for _, row := range r.Rows {
		if isSummaryOp(row.Operation) {
			return true
		}
	}
	return false
}

// Tools returns the distinct tools in first-seen order.
func (r *Report) Tools() []string { return distinct(r.Rows, func(row Row) string { return row.Tool }) }

// Scales returns the distinct tree sizes, ascending.
func (r *Report) Scales() []int {
	seen := map[int]bool{}
	var out []int
	for _, row := range r.Rows {
		if !seen[row.Scale] {
			seen[row.Scale] = true
			out = append(out, row.Scale)
		}
	}
	sort.Ints(out)
	return out
}

func distinct(rows []Row, key func(Row) string) []string {
	seen := map[string]bool{}
	var out []string
	for _, row := range rows {
		k := key(row)
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// cell reduces every sample for one point, and reports whether any was
// measured. A missing cell is normal: a tool may not support an operation, or a
// sweep may skip a size.
//
// A harness taking one sample per point gets a Stat whose median, min and max
// are that sample, so the single-sample and repeated-sample paths are the same
// code.
func (r *Report) cell(tool, op string, scale int) (Cell, bool) {
	var seconds, peak, alloc []float64
	var c Cell
	for _, row := range r.Rows {
		if row.Operation != op || row.Scale != scale || (tool != "" && row.Tool != tool) {
			continue
		}
		seconds = append(seconds, row.Seconds)
		peak = append(peak, row.PeakMB)
		// An unmeasured allocation total is absent, not zero: averaging a real
		// 300 MB with a placeholder 0 would report 150 MB and look plausible.
		if row.AllocMB > 0 {
			alloc = append(alloc, row.AllocMB)
		}
		if row.RepoDelta != "" {
			c.RepoDelta = row.RepoDelta
		}
	}
	if len(peak) == 0 {
		return Cell{}, false
	}
	c.Seconds, c.PeakMB, c.AllocMB = newStat(seconds), newStat(peak), newStat(alloc)
	c.HasAlloc = len(alloc) > 0
	return c, true
}

// hasRepoDelta reports whether any row measured repository growth. The memory
// sweep does not, so its table must not grow an empty column.
func (r *Report) hasRepoDelta() bool {
	for _, row := range r.Rows {
		if row.RepoDelta != "" && row.RepoDelta != "-" {
			return true
		}
	}
	return false
}

// hasAlloc reports whether any row carries an allocation total. Only the memory
// sweep measures it, and only for cloudstic, so the competitive table must not
// grow an empty column because of it.
func (r *Report) hasAlloc() bool {
	for _, row := range r.Rows {
		if row.AllocMB > 0 {
			return true
		}
	}
	return false
}

// Samples reports the largest number of repetitions behind any single point,
// which is what the prose quotes when explaining the median.
func (r *Report) Samples() int {
	counts := map[string]int{}
	most := 0
	for _, row := range r.Rows {
		k := fmt.Sprintf("%s\x00%s\x00%d", row.Tool, row.Operation, row.Scale)
		counts[k]++
		if counts[k] > most {
			most = counts[k]
		}
	}
	return most
}

// Metric selects which measurement a table or chart reports.
type Metric struct {
	Name  string // column heading
	Unit  string
	Value func(Cell) Stat
}

var (
	PeakMB  = Metric{Name: "Peak RSS", Unit: "MB", Value: func(c Cell) Stat { return c.PeakMB }}
	AllocMB = Metric{Name: "Total allocated", Unit: "MB", Value: func(c Cell) Stat { return c.AllocMB }}
	Seconds = Metric{Name: "Time", Unit: "s", Value: func(c Cell) Stat { return c.Seconds }}
)
