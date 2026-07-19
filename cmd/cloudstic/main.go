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
	cpuprofile, memprofile := parseProfileFlags()

	if len(os.Args) < 2 {
		printUsage(os.Stdout)
		return 1
	}

	if cpuprofile != "" {
		stop := startCPUProfile(cpuprofile)
		defer stop()
	}

	// A single cancellable context rooted here is threaded through every
	// command; SIGINT/SIGTERM cancels it so in-flight work (backup, restore,
	// remote store calls) can unwind cleanly instead of being hard-killed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	exitCode := runCmd(newRunner(os.Args[2:]), ctx, os.Args[1])

	if memprofile != "" {
		writeMemProfile(memprofile)
	}

	return exitCode
}

func runCmd(r *runner, ctx context.Context, cmd string) int {
	command := rootCommand(cmd)
	if command == nil {
		exitCode := r.fail("Unknown command: %s", cmd)
		if !r.jsonEnabled() {
			printUsage(r.errOut)
		}
		return exitCode
	}
	return dispatchCommand(r, ctx, command)
}

func versionCommandSpec() *commandSpec {
	return leafAliases("version", []string{"--version", "-v"}, "Display build version", "", runVersion).withoutFlagProbe()
}

func helpCommandSpec() *commandSpec {
	return leafAliases("help", []string{"--help", "-h"}, "Display help", "", runHelp).hide().withoutFlagProbe()
}

func runVersion(r *runner, _ context.Context) int {
	buildVersion, buildCommit, buildDate := buildMetadata()
	_, _ = fmt.Fprintf(r.out, "cloudstic %s (commit %s, built %s)\n", buildVersion, buildCommit, buildDate)
	return 0
}

func runHelp(r *runner, _ context.Context) int {
	printUsage(r.out)
	return 0
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
