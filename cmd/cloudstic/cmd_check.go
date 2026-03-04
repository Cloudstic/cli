package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
)

type checkArgs struct {
	g           *globalFlags
	readData    bool
	snapshotRef string
}

func parseCheckArgs() *checkArgs {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	a := &checkArgs{}
	a.g = addGlobalFlags(fs)
	readData := fs.Bool("read-data", false, "Re-hash all chunk data for full byte-level verification")
	snapshotFlag := fs.String("snapshot", "", "Check a specific snapshot (default: all)")
	mustParse(fs)
	a.readData = *readData
	a.snapshotRef = *snapshotFlag
	if a.snapshotRef == "" && fs.NArg() > 0 {
		a.snapshotRef = fs.Arg(0)
	}
	return a
}

func runCheck() {
	a := parseCheckArgs()

	ctx := context.Background()

	client, err := a.g.openClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}

	var checkOpts []cloudstic.CheckOption
	if a.readData {
		checkOpts = append(checkOpts, cloudstic.WithReadData())
	}
	if *a.g.verbose {
		checkOpts = append(checkOpts, cloudstic.WithCheckVerbose())
	}
	if a.snapshotRef != "" {
		checkOpts = append(checkOpts, cloudstic.WithSnapshotRef(a.snapshotRef))
	}

	result, err := client.Check(ctx, checkOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Check failed: %v\n", err)
		os.Exit(1)
	}

	if printCheckResult(result) {
		os.Exit(1)
	}
}

// printCheckResult prints the check summary to stderr.
// Returns true if integrity errors were found.
func printCheckResult(result *cloudstic.CheckResult) bool {
	fmt.Fprintf(os.Stderr, "\nRepository check complete.\n")
	fmt.Fprintf(os.Stderr, "  Snapshots checked:  %d\n", result.SnapshotsChecked)
	fmt.Fprintf(os.Stderr, "  Objects verified:   %d\n", result.ObjectsVerified)
	if len(result.Errors) > 0 {
		fmt.Fprintf(os.Stderr, "  Errors found:       %d\n\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", e.Type, e.Key, e.Message)
		}
		fmt.Fprintln(os.Stderr)
		return true
	}
	fmt.Fprintf(os.Stderr, "  Errors found:       0\n")
	fmt.Fprintf(os.Stderr, "\nNo errors found — repository is healthy.\n")
	return false
}
