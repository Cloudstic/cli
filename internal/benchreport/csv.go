package benchreport

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
)

var header = []string{"tool", "operation", "scale", "seconds", "peak_mb", "alloc_mb", "repo_delta"}

// AppendRow adds one measurement to path, writing the header first if the file
// is new.
//
// Appending a row at a time, rather than buffering a whole run, means an
// interrupted sweep still leaves everything it had measured. A benchmark that
// takes ten minutes and yields nothing when cancelled gets run less often.
func AppendRow(path string, row Row) error {
	_, statErr := os.Stat(path)
	isNew := os.IsNotExist(statErr)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	if isNew {
		if err := w.Write(header); err != nil {
			return err
		}
	}
	rec := []string{
		row.Tool,
		row.Operation,
		"",
		strconv.FormatFloat(row.Seconds, 'f', 2, 64),
		strconv.FormatFloat(row.PeakMB, 'f', 1, 64),
		"",
		row.RepoDelta,
	}
	if row.Scale > 0 {
		rec[2] = strconv.Itoa(row.Scale)
	}
	// Left empty rather than written as 0 when the harness did not measure it,
	// for the same reason as Scale: a zero reads as a real measurement, and
	// only cloudstic can report its own allocation total.
	if row.AllocMB > 0 {
		rec[5] = strconv.FormatFloat(row.AllocMB, 'f', 1, 64)
	}
	if err := w.Write(rec); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}
