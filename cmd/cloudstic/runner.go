package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
)

type runner struct {
	out      io.Writer
	errOut   io.Writer
	client   cloudsticClient
	noPrompt bool
	stdin    *os.File
	lineIn   *bufio.Reader
}

func newRunner() *runner {
	return &runner{
		out:      os.Stdout,
		errOut:   os.Stderr,
		noPrompt: hasGlobalFlag("no-prompt"),
		stdin:    os.Stdin,
	}
}

func (r *runner) lineReader() *bufio.Reader {
	if r.stdin == nil {
		r.stdin = os.Stdin
	}
	if r.lineIn == nil {
		r.lineIn = bufio.NewReader(r.stdin)
	}
	return r.lineIn
}

// hasGlobalFlag checks whether a boolean flag appears anywhere in os.Args.
// This is used for flags that must be parsed before subcommand flag sets.
func hasGlobalFlag(name string) bool {
	for _, arg := range os.Args[1:] {
		if arg == "-"+name || arg == "--"+name ||
			arg == "-"+name+"=true" || arg == "--"+name+"=true" {
			return true
		}
	}
	return false
}

// fail writes a formatted error to r.errOut and returns exit code 1.
func (r *runner) fail(format string, args ...any) int {
	_, _ = fmt.Fprintf(r.errOut, format+"\n", args...)
	return 1
}

// openClient opens the cloudstic client from the given global flags.
// No-op if r.client is already set (e.g. injected for tests).
func (r *runner) openClient(g *globalFlags) error {
	if r.client != nil {
		return nil
	}
	client, err := g.openClient()
	if err != nil {
		return err
	}
	r.client = client
	return nil
}
