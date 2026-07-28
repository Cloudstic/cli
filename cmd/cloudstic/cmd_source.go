package main

import (
	"context"
	"fmt"

	"github.com/jedib0t/go-pretty/v6/table"

	"github.com/cloudstic/cli/internal/workstation"
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

// discoverSources is the seam tests replace. Discovery reads the machine's
// real mount table, so without it the tests below would assert against
// whatever disks the developer or CI runner happens to have attached.
//
// It is a package-level var rather than a method on cloudsticClient because
// discovery needs nothing from the repository client — the method it used to
// hang off never touched its receiver.
var discoverSources = workstation.DiscoverSources

func runSourceDiscover(r *runner, ctx context.Context, a *sourceDiscoverArgs) int {
	results, err := discoverSources(ctx)
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
