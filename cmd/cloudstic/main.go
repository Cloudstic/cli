package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

func main() {
	os.Exit(run())
}

func run() int {
	prof := parseProfileFlags()

	if len(os.Args) < 2 {
		printUsage(os.Stdout)
		return 1
	}

	if prof.cpuProfile != "" {
		stop := startCPUProfile(prof.cpuProfile)
		defer stop()
	}

	// A single cancellable context rooted here is threaded through every
	// command; SIGINT/SIGTERM cancels it so in-flight work (backup, restore,
	// remote store calls) can unwind cleanly instead of being hard-killed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	exitCode := runCmd(newRunner(os.Args[2:]), ctx, os.Args[1])

	if prof.memProfile != "" {
		writeMemProfile(prof.memProfile)
	}
	if prof.memStats != "" {
		writeMemStats(prof.memStats)
	}

	return exitCode
}

func runCmd(r *runner, ctx context.Context, cmd string) int {
	switch cmd {
	case "version", "--version", "-v":
		buildVersion, buildCommit, buildDate := buildMetadata()
		_, _ = fmt.Fprintf(r.out, "cloudstic %s (commit %s, built %s)\n", buildVersion, buildCommit, buildDate)
		return 0
	case "help", "--help", "-h":
		printUsage(r.out)
		return 0
	}

	if c, ok := lookupCommand(cmd); ok {
		return c.execute(r, ctx, c.name)
	}

	exitCode := r.fail("Unknown command: %s", cmd)
	if !r.jsonEnabled() {
		printUsage(r.errOut)
	}
	return exitCode
}

func buildMetadata() (string, string, string) {
	info, _ := debug.ReadBuildInfo()
	return resolveBuildMetadata(info)
}

func resolveBuildMetadata(info *debug.BuildInfo) (string, string, string) {
	buildVersion, buildCommit, buildDate := "dev", "none", "unknown"
	if info != nil {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			buildVersion = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					buildCommit = setting.Value
				}
			case "vcs.time":
				if setting.Value != "" {
					buildDate = setting.Value
				}
			}
		}
	}
	return buildVersion, buildCommit, buildDate
}
