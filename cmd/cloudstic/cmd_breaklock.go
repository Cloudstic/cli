package main

import (
	"context"
	"flag"
	"fmt"
	"os"
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

func runBreakLock() {
	a := parseBreakLockArgs()

	ctx := context.Background()

	client, err := a.g.openClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}

	removed, err := client.BreakLock(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to break lock: %v\n", err)
		os.Exit(1)
	}

	if len(removed) == 0 {
		fmt.Fprintln(os.Stderr, "No lock found — repository is not locked.")
		return
	}

	fmt.Fprintf(os.Stderr, "Locks removed:\n")
	for _, r := range removed {
		fmt.Fprintf(os.Stderr, "  Operation:  %s\n", r.Operation)
		fmt.Fprintf(os.Stderr, "  Holder:     %s\n", r.Holder)
		fmt.Fprintf(os.Stderr, "  Acquired:   %s\n", r.AcquiredAt)
		fmt.Fprintf(os.Stderr, "  Expired at: %s\n", r.ExpiresAt)
		fmt.Fprintf(os.Stderr, "  Shared:     %v\n\n", r.IsShared)
	}
}
