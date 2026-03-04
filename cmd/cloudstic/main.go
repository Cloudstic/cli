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
	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("cloudstic %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "init":
		runInit()
	case "backup":
		runBackup()
	case "restore":
		runRestore()
	case "list":
		runList()
	case "ls":
		runLsSnapshot()
	case "prune":
		runPrune()
	case "forget":
		runForget()
	case "diff":
		runDiff()
	case "break-lock":
		runBreakLock()
	case "key":
		runKey()
	case "check":
		runCheck()
	case "cat":
		runCat()
	case "completion":
		runCompletion()
	case "help", "--help", "-h":
		printUsage()
		return 0
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		return 1
	}
	return 0
}
