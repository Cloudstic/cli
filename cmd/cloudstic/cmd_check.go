package main

import (
	"context"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type checkArgs struct {
	*globalFlags
	readData    bool
	snapshotRef string
}

func declareCheckArgs(g *globalFlags) (*checkArgs, commandInput) {
	a := &checkArgs{globalFlags: g}
	return a, commandInput{
		flags: []flagSpec{
			boolFlag(&a.readData, "read-data", false, "Re-hash all chunk data for full byte-level verification"),
		},
		positionals: []positionalSpec{optionalPositional(&a.snapshotRef, "snapshot ID", "latest")},
	}
}

func runCheck(r *runner, ctx context.Context, a *checkArgs, cfg clientConfig) int {
	if err := r.openClient(ctx, cfg); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	checkOpts := buildCheckOpts(a)

	result, err := r.client.Check(ctx, checkOpts...)
	if err != nil {
		return r.fail("Check failed: %v", err)
	}
	if a.jsonEnabled() {
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

// checkCommand declares the `check` command.
func checkCommand() command {
	return repoLeaf("check", "Verify repository integrity (reference chain, objects, data)",
		repoCommandGroups, declareCheckArgs, runCheck)
}
