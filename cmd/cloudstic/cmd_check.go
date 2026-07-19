package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type checkArgs struct {
	g           *globalFlags
	readData    bool
	snapshotRef string
}

func parseCheckArgs(args []string) (*checkArgs, error) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	a := &checkArgs{}
	a.g = addGlobalFlags(fs)
	readData := fs.Bool("read-data", false, "Re-hash all chunk data for full byte-level verification")
	if err := parseFlags(fs, args); err != nil {
		return nil, err
	}
	a.readData = *readData
	a.snapshotRef = fs.Arg(0)
	if a.snapshotRef == "" {
		a.snapshotRef = "latest"
	}
	return a, nil
}

func runCheck(r *runner, ctx context.Context) int {
	a, err := parseCheckArgs(r.args)
	if err != nil {
		return parseErrorExitCode(err)
	}
	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	checkOpts := buildCheckOpts(a)

	result, err := r.client.Check(ctx, checkOpts...)
	if err != nil {
		return r.fail("Check failed: %v", err)
	}
	if a.g.jsonEnabled() {
		if exit := r.writeJSON(result); exit != 0 {
			return exit
		}
		if len(result.Errors) > 0 {
			return 1
		}
		return 0
	}
	if printCheckResult(r.errOut, result) {
		return 1
	}
	return 0
}

func buildCheckOpts(a *checkArgs) []cloudstic.CheckOption {
	var checkOpts []cloudstic.CheckOption
	if a.readData {
		checkOpts = append(checkOpts, cloudstic.WithReadData())
	}
	if a.g.verbose {
		checkOpts = append(checkOpts, cloudstic.WithCheckVerbose())
	}
	if a.snapshotRef != "" {
		checkOpts = append(checkOpts, cloudstic.WithSnapshotRef(a.snapshotRef))
	}
	return checkOpts
}

// printCheckResult prints the check summary to r.errOut.
// Returns true if integrity errors were found.
func printCheckResult(errOut io.Writer, result *cloudstic.CheckResult) bool {
	_, _ = fmt.Fprintf(errOut, "\nRepository check complete.\n")
	_, _ = fmt.Fprintf(errOut, "  Snapshots checked:  %d\n", result.SnapshotsChecked)
	_, _ = fmt.Fprintf(errOut, "  Objects verified:   %d\n", result.ObjectsVerified)
	if len(result.Errors) > 0 {
		_, _ = fmt.Fprintf(errOut, "  Errors found:       %d\n\n", len(result.Errors))
		for _, e := range result.Errors {
			_, _ = fmt.Fprintf(errOut, "  [%s] %s: %s\n", e.Type, e.Key, e.Message)
		}
		_, _ = fmt.Fprintln(errOut)
		return true
	}
	_, _ = fmt.Fprintf(errOut, "  Errors found:       0\n")
	_, _ = fmt.Fprintf(errOut, "\nNo errors found — repository is healthy.\n")
	return false
}
