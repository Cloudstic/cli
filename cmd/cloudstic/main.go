package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cpuprofile, memprofile := parseProfileFlags()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
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

	exitCode := runCmd(ctx, os.Args[1], os.Args[2:])

	if memprofile != "" {
		writeMemProfile(memprofile)
	}

	os.Exit(exitCode)
}

func runCmd(ctx context.Context, cmd string, args []string) int {
	r := newRunner(args)
	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("cloudstic %s (commit %s, built %s)\n", version, commit, date)
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
		printUsage()
		return 0
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		return 1
	}
}
