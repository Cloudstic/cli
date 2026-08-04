// Package benchreport turns benchmark measurements into a Markdown report.
//
// The measurement scripts orchestrate external binaries, which is what shell is
// good at. Everything after that — arithmetic, grouping, table and chart
// layout — is what shell is worst at, and is here instead: it is typed, it is
// tested, and it does not fork bc to divide two numbers.
package benchreport

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Row is one measured operation.
//
// One schema serves both harnesses because they differ only in which column
// varies. The memory sweep holds Tool constant and varies Scale; the
// competitive run holds Scale constant and varies Tool. Kind() reads that off
// the data rather than being told.
type Row struct {
	Tool      string // cloudstic, restic, borg, duplicacy
	Operation string // "backup-initial", "Initial Backup", …
	Scale     int    // files in tree; 0 when the harness has only one size
	Seconds   float64
	PeakMB    float64
	RepoDelta string // human-formatted, passed through untouched
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
// order the harness ran them and therefore the order a reader expects.
func (r *Report) Operations() []string {
	return distinct(r.Rows, func(row Row) string { return row.Operation })
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

// find returns the row for one cell, and whether it was measured. A missing
// cell is normal: a tool may not support an operation, or a sweep may skip a
// size.
func (r *Report) find(tool, op string, scale int) (Row, bool) {
	for _, row := range r.Rows {
		if row.Operation == op && row.Scale == scale && (tool == "" || row.Tool == tool) {
			return row, true
		}
	}
	return Row{}, false
}

// Metric selects which measurement a table or chart reports.
type Metric struct {
	Name  string // column heading
	Unit  string
	Value func(Row) float64
}

var (
	PeakMB  = Metric{Name: "Peak RSS", Unit: "MB", Value: func(r Row) float64 { return r.PeakMB }}
	Seconds = Metric{Name: "Time", Unit: "s", Value: func(r Row) float64 { return r.Seconds }}
)
