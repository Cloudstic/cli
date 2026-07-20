package main

import (
	"context"
	"fmt"

	cloudstic "github.com/cloudstic/cli"
	"github.com/jedib0t/go-pretty/v6/table"
)

type sourceDiscoverArgs struct {
	portableOnly bool
	jsonOutput   bool
}

func declareSourceDiscoverArgs(_ *globalFlags) (*sourceDiscoverArgs, commandInput) {
	a := &sourceDiscoverArgs{}
	return a, commandInput{flags: []flagSpec{
		boolFlag(&a.portableOnly, "portable-only", false, "Only show portable/external source candidates"),
		boolFlag(&a.jsonOutput, "json", false, "Write discovered sources as JSON"),
	}}
}

func runSourceDiscover(r *runner, ctx context.Context, a *sourceDiscoverArgs) int {
	if r.client == nil {
		r.client = &cloudstic.Client{}
	}

	results, err := r.client.DiscoverSources(ctx)
	if err != nil {
		return r.fail("Failed to discover sources: %v", err)
	}

	if a.portableOnly {
		filtered := results[:0]
		for _, result := range results {
			if result.Portable {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}

	if a.jsonOutput {
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

// sourceCommand declares the `source` command group.
func sourceCommand() command {
	return group("source", "Discover source candidates for onboarding",
		leaf("discover", "Discover local source candidates for onboarding", nil, declareSourceDiscoverArgs, runSourceDiscover),
	)
}
