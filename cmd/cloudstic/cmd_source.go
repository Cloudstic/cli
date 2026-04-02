package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/cloudstic/cli/pkg/source"
	"github.com/jedib0t/go-pretty/v6/table"
)

var discoverSources = source.DiscoverSources

func (r *runner) runSource(ctx context.Context) int {
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic source <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: discover")
		return 1
	}

	switch os.Args[2] {
	case "discover":
		return r.runSourceDiscover(ctx)
	default:
		return r.fail("Unknown source subcommand: %s", os.Args[2])
	}
}

func (r *runner) runSourceDiscover(ctx context.Context) int {
	_ = ctx
	fs := flag.NewFlagSet("source discover", flag.ExitOnError)
	portableOnly := fs.Bool("portable-only", false, "Only show portable/external source candidates")
	jsonOutput := fs.Bool("json", false, "Write discovered sources as JSON")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))

	results, err := discoverSources()
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
