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
	switch cmd {
	case "version", "--version", "-v":
		buildVersion, buildCommit, buildDate := buildMetadata()
		_, _ = fmt.Fprintf(r.out, "cloudstic %s (commit %s, built %s)\n", buildVersion, buildCommit, buildDate)
		return 0
	case "init":
		return runInit(r, ctx)
	case "backup":
		return runBackup(r, ctx)
	case "restore":
		return runRestore(r, ctx)
	case "list":
		return runList(r, ctx)
	case "ls":
		return runLsSnapshot(r, ctx)
	case "prune":
		return runPrune(r, ctx)
	case "forget":
		return runForget(r, ctx)
	case "diff":
		return runDiff(r, ctx)
	case "break-lock":
		return runBreakLock(r, ctx)
	case "key":
		return runKey(r, ctx)
	case "check":
		return runCheck(r, ctx)
	case "cat":
		return runCat(r, ctx)
	case "profile":
		return runProfile(r, ctx)
	case "auth":
		return runAuth(r, ctx)
	case "store":
		return runStore(r, ctx)
	case "source":
		return runSource(r, ctx)
	case "setup":
		return runSetup(r, ctx)
	case "tui":
		return runTUI(r, ctx)
	case "completion":
		return runCompletion(r)
	case "__complete":
		return runCompletionQuery(r, ctx)
	case "help", "--help", "-h":
		printUsage(r.out)
		return 0
	default:
		exitCode := r.fail("Unknown command: %s", cmd)
		if !r.jsonEnabled() {
			printUsage(r.errOut)
		}
		return exitCode
	}
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
