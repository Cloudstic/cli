package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type catArgs struct {
	g    *globalFlags
	keys []string
	raw  bool
}

func catFlagSpecs(a *catArgs) []flagSpec {
	return []flagSpec{
		boolFlag(&a.raw, "raw", false, "Output raw, unformatted data (useful for hashing)"),
	}
}

func newCatFlagSet() (*flag.FlagSet, *catArgs) {
	fs := flag.NewFlagSet("cat", flag.ContinueOnError)
	a := &catArgs{}
	a.g = addGlobalFlags(fs, repoCommandGroups)
	bindFlags(fs, catFlagSpecs(a))
	return fs, a
}

func parseCatArgs(args []string) (*catArgs, error) {
	fs, a := newCatFlagSet()
	if err := parseFlags(fs, args); err != nil {
		return nil, err
	}
	if fs.NArg() < 1 {
		return nil, fmt.Errorf("at least one object key is required")
	}
	a.keys = fs.Args()
	return a, nil
}

func runCat(r *runner, ctx context.Context) int {
	a, err := parseCatArgs(r.args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if !r.jsonEnabled() {
			printCatUsage(r.errOut)
		}
		return r.parseError(err)
	}
	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	if a.g.jsonEnabled() && a.raw {
		return r.failJSONFlagConflict("-json", "-raw")
	}

	quiet := a.g.quiet || a.g.jsonEnabled()

	results, err := r.client.Cat(ctx, a.keys...)
	if err != nil {
		return r.fail("Failed to fetch objects: %v", err)
	}

	if a.g.jsonEnabled() {
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
