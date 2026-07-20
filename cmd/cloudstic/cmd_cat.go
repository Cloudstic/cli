package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type catArgs struct {
	*globalFlags
	keys []string
	raw  bool
}

func declareCatArgs(g *globalFlags) (*catArgs, commandInput) {
	a := &catArgs{globalFlags: g}
	return a, commandInput{
		flags: []flagSpec{
			boolFlag(&a.raw, "raw", false, "Output raw, unformatted data (useful for hashing)"),
		},
		positionals: []positionalSpec{requiredPositionals(&a.keys, "object key")},
	}
}

func runCat(r *runner, ctx context.Context, a *catArgs) int {
	if err := r.openClient(ctx, a.globalFlags); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	if a.jsonEnabled() && a.raw {
		return r.failJSONFlagConflict("-json", "-raw")
	}

	quiet := a.quiet || a.jsonEnabled()

	results, err := r.client.Cat(ctx, a.keys...)
	if err != nil {
		return r.fail("Failed to fetch objects: %v", err)
	}

	if a.jsonEnabled() {
		return r.writeJSON(makeCatJSONResults(results))
	}
	printCatResult(r.out, r.errOut, results, quiet, a.raw)
	return 0
}

func printCatUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: cloudstic cat [options] <object_key> [object_key...]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Examples:")
	_, _ = fmt.Fprintln(w, "  cloudstic cat config")
	_, _ = fmt.Fprintln(w, "  cloudstic cat index/latest")
	_, _ = fmt.Fprintln(w, "  cloudstic cat snapshot/abc123...")
	_, _ = fmt.Fprintln(w, "  cloudstic cat filemeta/def456... node/789abc...")
	_, _ = fmt.Fprintln(w, "  cloudstic cat -raw filemeta/def456... | sha256sum")
}

func printCatResult(out io.Writer, errOut io.Writer, results []*cloudstic.CatResult, quiet, raw bool) {
	for i, result := range results {
		if !quiet && len(results) > 1 {
			_, _ = fmt.Fprintf(errOut, "==> %s <==\n", result.Key)
		}
		if raw {
			if _, err := out.Write(result.Data); err != nil {
				_, _ = fmt.Fprintf(errOut, "Failed to write raw data: %v\n", err)
				return
			}
		} else {
			var indented bytes.Buffer
			if err := json.Indent(&indented, result.Data, "", "  "); err != nil {
				_, _ = fmt.Fprint(out, string(result.Data))
			} else {
				_, _ = fmt.Fprintln(out, indented.String())
			}
		}

		if !quiet && i < len(results)-1 {
			_, _ = fmt.Fprintln(errOut)
		}
	}
}

// catCommand declares the `cat` command.
func catCommand() command {
	return leaf("cat", "Display raw JSON content of repository objects",
		repoCommandGroups, declareCatArgs, runCat, withUsageOnError(printCatUsage))
}
