package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type breakLockArgs struct {
	g *globalFlags
}

func newBreakLockFlagSet() (boundFlagSet, *breakLockArgs) {
	fs := flag.NewFlagSet("break-lock", flag.ContinueOnError)
	g, globalSpecs := addGlobalFlags(fs, repoCommandGroups)
	return boundFlagSet{set: fs, global: globalSpecs}, &breakLockArgs{g: g}
}

func parseBreakLockArgs(args []string) (*breakLockArgs, error) {
	b, a := newBreakLockFlagSet()
	if err := parseFlags(b, args); err != nil {
		return nil, err
	}
	return a, nil
}

func runBreakLock(r *runner, ctx context.Context) int {
	a, err := parseBreakLockArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}
	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	removed, err := r.client.BreakLock(ctx)
	if err != nil {
		return r.fail("Failed to break lock: %v", err)
	}
	if a.g.jsonEnabled() {
		return r.writeJSON(&breakLockJSONResult{Locks: removed})
	}
	printBreakLockResult(r.errOut, removed)
	return 0
}

func printBreakLockResult(errOut io.Writer, removed []*cloudstic.RepoLock) {
	if len(removed) == 0 {
		_, _ = fmt.Fprintln(errOut, "No lock found — repository is not locked.")
		return
	}

	_, _ = fmt.Fprintf(errOut, "Locks removed:\n")
	for _, lock := range removed {
		_, _ = fmt.Fprintf(errOut, "  Operation:  %s\n", lock.Operation)
		_, _ = fmt.Fprintf(errOut, "  Holder:     %s\n", lock.Holder)
		_, _ = fmt.Fprintf(errOut, "  Acquired:   %s\n", lock.AcquiredAt)
		_, _ = fmt.Fprintf(errOut, "  Expired at: %s\n", lock.ExpiresAt)
		_, _ = fmt.Fprintf(errOut, "  Shared:     %v\n\n", lock.IsShared)
	}
}
