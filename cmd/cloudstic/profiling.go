package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
)

// profileFlags are the measurement knobs, stripped from os.Args before the
// ordinary flag parsing runs.
//
// They are deliberately handled here rather than declared as flagSpecs: they
// belong to the benchmark harness, not to the command surface, and routing them
// through flagspec.go would put them in `-h` output, shell completion and the
// help golden files for every command.
type profileFlags struct {
	cpuProfile string
	memProfile string
	memStats   string
}

// parseProfileFlags strips -cpuprofile, -memprofile and -memstats from os.Args
// and returns their values. os.Args is updated in place with those flags
// removed so the rest of the CLI flag parsing is unaffected.
func parseProfileFlags() profileFlags {
	var p profileFlags
	// Each knob is a name and where its value lands, so adding one is a line
	// rather than another pair of switch arms.
	knobs := []struct {
		name string
		dst  *string
	}{
		{"-cpuprofile", &p.cpuProfile},
		{"-memprofile", &p.memProfile},
		{"-memstats", &p.memStats},
	}

	newArgs := []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		matched := false
		for _, k := range knobs {
			switch {
			case strings.HasPrefix(a, k.name+"="):
				*k.dst = strings.TrimPrefix(a, k.name+"=")
				matched = true
			case a == k.name && i+1 < len(os.Args):
				i++
				*k.dst = os.Args[i]
				matched = true
			}
			if matched {
				break
			}
		}
		if !matched {
			newArgs = append(newArgs, a)
		}
	}
	os.Args = newArgs
	return p
}

func startCPUProfile(path string) (stop func()) {
	runtime.SetBlockProfileRate(1)
	runtime.SetMutexProfileFraction(1)

	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not create CPU profile: %v\n", err)
		os.Exit(1)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		fmt.Fprintf(os.Stderr, "could not start CPU profile: %v\n", err)
		os.Exit(1)
	}

	return func() {
		pprof.StopCPUProfile()
		_ = f.Close()

		// Dump goroutine, block, and mutex profiles alongside the CPU profile.
		for _, p := range []struct {
			name string
			ext  string
			dbg  int
		}{
			{"goroutine", ".goroutine", 1},
			{"block", ".block", 0},
			{"mutex", ".mutex", 0},
		} {
			if pf, err := os.Create(path + p.ext); err == nil {
				_ = pprof.Lookup(p.name).WriteTo(pf, p.dbg)
				_ = pf.Close()
			}
		}
	}
}

// memStats is the allocation summary written by -memstats.
//
// Peak RSS answers "how much was live at once" and is blind to allocation that
// is freed again: a change removing 36 MB of transient garbage moves TotalAlloc
// by 36 MB and peak RSS by nothing measurable. The benchmark harness reports
// both for that reason.
//
// The counters come from runtime.MemStats rather than the two alternatives:
//
//   - A heap profile (-memprofile) read with `go tool pprof -alloc_space` is
//     *sampled* — one sample per 512 KB by default — so a small delta lands in
//     the sampling error, and collecting it perturbs the RSS being measured in
//     the same pass. Measuring the two separately would double the harness's
//     runtime.
//   - GODEBUG=gctrace=1 emits a debug format the Go release notes explicitly
//     decline to keep stable, and reports heap size at each GC rather than a
//     cumulative total, which has to be reconstructed by summing.
//
// TotalAlloc is a counter the runtime maintains regardless. Reading it is
// exact, costs nothing, needs no parsing, and happens in the same run that
// produced the RSS number beside it.
type memStats struct {
	TotalAllocBytes uint64 `json:"total_alloc_bytes"` // cumulative, including freed
	Mallocs         uint64 `json:"mallocs"`
	HeapSysBytes    uint64 `json:"heap_sys_bytes"`
	NumGC           uint32 `json:"num_gc"`
}

func writeMemStats(path string) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	data, err := json.Marshal(memStats{
		TotalAllocBytes: ms.TotalAlloc,
		Mallocs:         ms.Mallocs,
		HeapSysBytes:    ms.HeapSys,
		NumGC:           ms.NumGC,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not encode memory stats: %v\n", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "could not write memory stats: %v\n", err)
	}
}

func writeMemProfile(path string) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not create memory profile: %v\n", err)
		return
	}
	defer func() { _ = f.Close() }()
	if err := pprof.WriteHeapProfile(f); err != nil {
		fmt.Fprintf(os.Stderr, "could not write memory profile: %v\n", err)
	}
}
