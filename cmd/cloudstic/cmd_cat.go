package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
)

type catArgs struct {
	g    *globalFlags
	keys []string
	json bool
	raw  bool
}

func parseCatArgs() *catArgs {
	fs := flag.NewFlagSet("cat", flag.ExitOnError)
	a := &catArgs{}
	a.g = addGlobalFlags(fs)
	jsonFlag := fs.Bool("json", false, "Suppress non-JSON output (alias for -quiet)")
	rawFlag := fs.Bool("raw", false, "Output raw, unformatted data (useful for hashing)")
	mustParse(fs)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: cloudstic cat [options] <object_key> [object_key...]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  cloudstic cat config")
		fmt.Fprintln(os.Stderr, "  cloudstic cat index/latest")
		fmt.Fprintln(os.Stderr, "  cloudstic cat snapshot/abc123...")
		fmt.Fprintln(os.Stderr, "  cloudstic cat filemeta/def456... node/789abc...")
		fmt.Fprintln(os.Stderr, "  cloudstic cat -raw filemeta/def456... | sha256sum")
		os.Exit(1)
	}
	a.json = *jsonFlag
	a.raw = *rawFlag
	a.keys = fs.Args()
	return a
}

func (r *runner) runCat() int {
	a := parseCatArgs()
	if err := r.openClient(a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	quiet := *a.g.quiet || a.json

	results, err := r.client.Cat(context.Background(), a.keys...)
	if err != nil {
		return r.fail("Failed to fetch objects: %v", err)
	}

	r.printCatResult(results, quiet, a.raw)
	return 0
}

func (r *runner) printCatResult(results []*cloudstic.CatResult, quiet, raw bool) {
	for i, result := range results {
		if !quiet && len(results) > 1 {
			_, _ = fmt.Fprintf(r.errOut, "==> %s <==\n", result.Key)
		}
		if raw {
			if _, err := r.out.Write(result.Data); err != nil {
				_, _ = fmt.Fprintf(r.errOut, "Failed to write raw data: %v\n", err)
				return
			}
		} else {
			var indented bytes.Buffer
			if err := json.Indent(&indented, result.Data, "", "  "); err != nil {
				_, _ = fmt.Fprint(r.out, string(result.Data))
			} else {
				_, _ = fmt.Fprintln(r.out, indented.String())
			}
		}

		if !quiet && i < len(results)-1 {
			_, _ = fmt.Fprintln(r.errOut)
		}
	}
}
