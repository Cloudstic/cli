package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
)

// parseProfileFlags strips -cpuprofile and -memprofile from os.Args and
// returns their values. os.Args is updated in place with those flags removed
// so the rest of the CLI flag parsing is unaffected.
func parseProfileFlags() (cpuprofile, memprofile string) {
	var newArgs []string
	newArgs = append(newArgs, os.Args[0])
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		switch {
		case strings.HasPrefix(a, "-cpuprofile="):
			cpuprofile = strings.TrimPrefix(a, "-cpuprofile=")
		case a == "-cpuprofile" && i+1 < len(os.Args):
			i++
			cpuprofile = os.Args[i]
		case strings.HasPrefix(a, "-memprofile="):
			memprofile = strings.TrimPrefix(a, "-memprofile=")
		case a == "-memprofile" && i+1 < len(os.Args):
			i++
			memprofile = os.Args[i]
		default:
			newArgs = append(newArgs, a)
		}
	}
	os.Args = newArgs
	return
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
