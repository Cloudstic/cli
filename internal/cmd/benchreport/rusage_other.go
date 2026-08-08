//go:build !unix

package main

import (
	"fmt"
	"os"
	"runtime"
)

// peakRSSBytes has no portable equivalent outside unix. The benchmark harness
// only runs on the macOS and Linux runners, so this reports the gap rather
// than pretending to a number.
func peakRSSBytes(_ *os.ProcessState) (int64, error) {
	return 0, fmt.Errorf("peak RSS measurement is not implemented on %s", runtime.GOOS)
}
