package main

import (
	"context"
	"fmt"
	"os"
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

	exitCode := runCmd(os.Args[1])

	if memprofile != "" {
		writeMemProfile(memprofile)
	}

	os.Exit(exitCode)
}

func runCmd(cmd string) int {
	r := newRunner()
	ctx := context.Background()
	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("cloudstic %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "init":
		return r.runInit(ctx)
	case "backup":
		return r.runBackup(ctx)
	case "restore":
		return r.runRestore(ctx)
	case "list":
		return r.runList(ctx)
	case "ls":
		return r.runLsSnapshot(ctx)
	case "prune":
		return r.runPrune(ctx)
	case "forget":
		return r.runForget(ctx)
	case "diff":
		return r.runDiff(ctx)
	case "break-lock":
		return r.runBreakLock(ctx)
	case "key":
		return r.runKey(ctx)
	case "check":
		return r.runCheck(ctx)
	case "cat":
		return r.runCat(ctx)
	case "profile":
		return r.runProfile(ctx)
	case "auth":
		return r.runAuth(ctx)
	case "store":
		return r.runStore(ctx)
	case "source":
		return r.runSource(ctx)
	case "setup":
		return r.runSetup(ctx)
	case "tui":
		return r.runTUI(ctx)
	case "completion":
		runCompletion()
		return 0
	case "__complete":
		return runCompletionQuery(ctx)
	case "help", "--help", "-h":
		printUsage()
		return 0
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		return 1
	}
}
