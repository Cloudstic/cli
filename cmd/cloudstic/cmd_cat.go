package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

type catArgs struct {
	g    *globalFlags
	keys []string
	json bool
}

func parseCatArgs() *catArgs {
	fs := flag.NewFlagSet("cat", flag.ExitOnError)
	a := &catArgs{}
	a.g = addGlobalFlags(fs)
	jsonFlag := fs.Bool("json", false, "Suppress non-JSON output (alias for -quiet)")
	mustParse(fs)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: cloudstic cat [options] <object_key> [object_key...]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  cloudstic cat config")
		fmt.Fprintln(os.Stderr, "  cloudstic cat index/latest")
		fmt.Fprintln(os.Stderr, "  cloudstic cat snapshot/abc123...")
		fmt.Fprintln(os.Stderr, "  cloudstic cat filemeta/def456... node/789abc...")
		os.Exit(1)
	}
	a.json = *jsonFlag
	a.keys = fs.Args()
	return a
}

func runCat() {
	a := parseCatArgs()

	quiet := *a.g.quiet || a.json

	ctx := context.Background()

	client, err := a.g.openClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}

	results, err := client.Cat(ctx, a.keys...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to fetch objects: %v\n", err)
		os.Exit(1)
	}

	for i, result := range results {
		if !quiet && len(results) > 1 {
			fmt.Fprintf(os.Stderr, "==> %s <==\n", result.Key)
		}

		// Pretty-print JSON
		var indented bytes.Buffer
		if err := json.Indent(&indented, result.Data, "", "  "); err != nil {
			// If it's not valid JSON, just output the raw data
			fmt.Print(string(result.Data))
		} else {
			fmt.Println(indented.String())
		}

		// Add spacing between multiple objects
		if !quiet && i < len(results)-1 {
			fmt.Fprintln(os.Stderr)
		}
	}
}
