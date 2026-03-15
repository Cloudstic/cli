package main

import (
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
	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("cloudstic %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "init":
		return r.runInit()
	case "backup":
		return r.runBackup()
	case "restore":
		return r.runRestore()
	case "list":
		return r.runList()
	case "ls":
		return r.runLsSnapshot()
	case "prune":
		return r.runPrune()
	case "forget":
		return r.runForget()
	case "diff":
		return r.runDiff()
	case "break-lock":
		return r.runBreakLock()
	case "key":
		return r.runKey()
	case "check":
		return r.runCheck()
	case "cat":
		return r.runCat()
	case "profile":
		return r.runProfile()
	case "auth":
		return r.runAuth()
	case "store":
		return r.runStore()
	case "completion":
		runCompletion()
		return 0
	case "help", "--help", "-h":
		printUsage()
		return 0
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		return 1
	}
}
