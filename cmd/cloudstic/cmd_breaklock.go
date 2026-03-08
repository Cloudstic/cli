package main

import (
	"context"
	"flag"
	"fmt"

	cloudstic "github.com/cloudstic/cli"
)

type breakLockArgs struct {
	g *globalFlags
}

func parseBreakLockArgs() *breakLockArgs {
	fs := flag.NewFlagSet("break-lock", flag.ExitOnError)
	a := &breakLockArgs{g: addGlobalFlags(fs)}
	mustParse(fs)
	return a
}

func (r *runner) runBreakLock() int {
	a := parseBreakLockArgs()
	if err := r.openClient(a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	removed, err := r.client.BreakLock(context.Background())
	if err != nil {
		return r.fail("Failed to break lock: %v", err)
	}
	r.printBreakLockResult(removed)
	return 0
}

func (r *runner) printBreakLockResult(removed []*cloudstic.RepoLock) {
	if len(removed) == 0 {
		_, _ = fmt.Fprintln(r.errOut, "No lock found — repository is not locked.")
		return
	}

	_, _ = fmt.Fprintf(r.errOut, "Locks removed:\n")
	for _, lock := range removed {
		_, _ = fmt.Fprintf(r.errOut, "  Operation:  %s\n", lock.Operation)
		_, _ = fmt.Fprintf(r.errOut, "  Holder:     %s\n", lock.Holder)
		_, _ = fmt.Fprintf(r.errOut, "  Acquired:   %s\n", lock.AcquiredAt)
		_, _ = fmt.Fprintf(r.errOut, "  Expired at: %s\n", lock.ExpiresAt)
		_, _ = fmt.Fprintf(r.errOut, "  Shared:     %v\n\n", lock.IsShared)
	}
}
