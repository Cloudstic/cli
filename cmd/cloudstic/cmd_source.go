package main

import (
	"context"
	"flag"
	"fmt"

	cloudstic "github.com/cloudstic/cli"
	"github.com/jedib0t/go-pretty/v6/table"
)

func sourceCommandSpec() *commandSpec {
	return group("source", "Discover source candidates for onboarding", sourceDiscoverCommandSpec())
}

func sourceDiscoverCommandSpec() *commandSpec {
	return leaf("discover", "Discover local source candidates", "", runSourceDiscover,
		boolFlag("portable-only", "Only show portable source candidates"),
		boolFlag("json", "Write sources as JSON"))
}

func runSourceDiscover(r *runner, ctx context.Context) int {
	fs := flag.NewFlagSet("source discover", flag.ContinueOnError)
	portableOnly := fs.Bool("portable-only", false, "Only show portable/external source candidates")
	jsonOutput := fs.Bool("json", false, "Write discovered sources as JSON")
	if err := parseFlags(fs, r.args, sourceDiscoverCommandSpec()); err != nil {
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
