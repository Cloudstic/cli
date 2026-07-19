package main

import (
	"context"
	"flag"
	"fmt"

	cloudstic "github.com/cloudstic/cli"
	"github.com/jedib0t/go-pretty/v6/table"
)

func runSource(r *runner, ctx context.Context) int {
	if len(r.args) < 1 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic source <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: discover")
		return 1
	}

	subcommand := r.args[0]
	subRunner := r.withArgs(r.args[1:])
	switch subcommand {
	case "discover":
		return runSourceDiscover(subRunner, ctx)
	default:
		return r.fail("Unknown source subcommand: %s", subcommand)
	}
}

func runSourceDiscover(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("source discover", flag.ContinueOnError)
	portableOnly := fs.Bool("portable-only", false, "Only show portable/external source candidates")
	jsonOutput := fs.Bool("json", false, "Write discovered sources as JSON")
	if err := parseFlags(fs, r.args); err != nil {
		return r.parseError(err)
	}

	if r.client == nil {
		r.client = &cloudstic.Client{}
	}

	results, err := r.client.DiscoverSources(ctx)
	if err != nil {
		return r.fail("Failed to discover sources: %v", err)
	}

	if *portableOnly {
		filtered := results[:0]
		for _, result := range results {
			if result.Portable {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}

	if *jsonOutput {
		return r.writeJSON(results)
	}

	if len(results) == 0 {
		_, _ = fmt.Fprintln(r.out, "No sources discovered.")
		return 0
	}

	t := table.NewWriter()
	t.SetOutputMirror(r.out)
	t.AppendHeader(table.Row{"Name", "Source URI", "Mount", "Identity", "FS", "Portable"})
	for _, result := range results {
		t.AppendRow(table.Row{
			result.DisplayName,
			result.SourceURI,
			result.MountPoint,
			result.Identity,
			result.FsType,
			boolLabel(result.Portable),
		})
	}
	t.Render()
	return 0
}
