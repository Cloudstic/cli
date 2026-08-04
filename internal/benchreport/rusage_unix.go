//go:build unix

package benchreport

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// peakRSSBytes reads maximum resident set size out of the wait4 rusage the
// kernel already handed us.
//
// The unit is the one thing that genuinely varies: Darwin reports bytes, while
// Linux and the BSDs report kilobytes. That is a two-line switch here rather
// than two /usr/bin/time parsing branches in shell.
func peakRSSBytes(st *os.ProcessState) (int64, error) {
	usage, ok := st.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0, fmt.Errorf("rusage unavailable on %s", runtime.GOOS)
	}
	if runtime.GOOS == "darwin" {
		return int64(usage.Maxrss), nil
	}
	return int64(usage.Maxrss) * 1024, nil
}
