package benchreport

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Measure runs cmd to completion and reports how long it took and how much
// resident memory it peaked at.
//
// This replaces parsing /usr/bin/time, which reports maximum resident set size
// under a different flag, a different label and a different unit per platform
// — `-l` in bytes on BSD, `-v` in kilobytes on GNU — and is absent altogether
// on a minimal Linux image. The kernel hands the same number to the parent
// through wait4; taking it from there removes the fork, the two parsing
// branches and the dependency.
func Measure(name string, args []string, stdout, stderr io.Writer) (seconds float64, peakMB float64, err error) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()

	start := time.Now()
	if err := cmd.Run(); err != nil {
		return 0, 0, fmt.Errorf("%s: %w", name, err)
	}
	elapsed := time.Since(start)

	bytes, err := peakRSSBytes(cmd.ProcessState)
	if err != nil {
		return elapsed.Seconds(), 0, err
	}
	return elapsed.Seconds(), float64(bytes) / (1024 * 1024), nil
}

// ReadAllocMB reads the cumulative allocation total from the file cloudstic
// wrote for -memstats.
//
// Peak RSS is a high-water mark, so it says nothing about allocation the
// collector reclaimed before the peak — which is where most of the churn in a
// backup lives. The measured process reports its own runtime.MemStats.TotalAlloc
// because that counter is exact and free, where a sampled heap profile would put
// the change inside its own sampling error and perturb the RSS beside it. See
// cmd/cloudstic/profiling.go.
func ReadAllocMB(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read memstats: %w", err)
	}
	var ms struct {
		TotalAllocBytes uint64 `json:"total_alloc_bytes"`
	}
	if err := json.Unmarshal(data, &ms); err != nil {
		return 0, fmt.Errorf("parse memstats %s: %w", path, err)
	}
	if ms.TotalAllocBytes == 0 {
		return 0, fmt.Errorf("memstats %s reports no allocation; was -memstats passed to the measured command?", path)
	}
	return float64(ms.TotalAllocBytes) / (1024 * 1024), nil
}
