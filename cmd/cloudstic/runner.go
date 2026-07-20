package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

const (
	exitFailure     = 1
	exitInterrupted = 130
)

type errorOutput struct {
	Error string `json:"error"`
}

type runner struct {
	args              []string
	out               io.Writer
	errOut            io.Writer
	stdoutFile        *os.File
	client            cloudsticClient
	noPrompt          bool
	stdin             *os.File
	lineIn            *bufio.Reader
	runInteractiveCmd func(context.Context, *os.File, io.Writer, io.Writer, string, ...string) error
	usage             func(io.Writer)
}

func newRunner(args []string) *runner {
	return &runner{
		args:              args,
		out:               os.Stdout,
		errOut:            os.Stderr,
		stdoutFile:        os.Stdout,
		noPrompt:          hasGlobalFlag(args, "no-prompt"),
		stdin:             os.Stdin,
		runInteractiveCmd: defaultRunInteractiveCmd,
	}
}

// withArgs returns a shallow runner copy for nested command dispatch. The
// parent runner and its argument slice are never mutated.
func (r *runner) withArgs(args []string) *runner {
	child := *r
	child.args = args
	child.noPrompt = r.noPrompt || hasGlobalFlag(args, "no-prompt")
	return &child
}

// withUsage returns a shallow runner copy with the current command's derived
// usage renderer attached for semantic validation errors.
func (r *runner) withUsage(print func(io.Writer)) *runner {
	child := *r
	child.usage = print
	return &child
}

func (r *runner) printUsage(w io.Writer) {
	if r.usage != nil {
		r.usage(w)
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

// hasGlobalFlag checks whether a boolean flag appears before the -- terminator.
// This is used for flags that must be parsed before subcommand flag sets.
func hasGlobalFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "-"+name || arg == "--"+name ||
			arg == "-"+name+"=true" || arg == "--"+name+"=true" {
			return true
		}
	}
	return false
}

func (r *runner) jsonEnabled() bool {
	return hasGlobalFlag(r.args, "json")
}

// fail reports an error using the command's selected output format.
func (r *runner) fail(format string, args ...any) int {
	message, exitCode := fmt.Sprintf(format, args...), exitFailure
	for _, arg := range args {
		err, ok := arg.(error)
		if ok && errors.Is(err, context.Canceled) {
			message, exitCode = "Interrupted.", exitInterrupted
			break
		}
	}
	if r.jsonEnabled() {
		_ = json.NewEncoder(r.errOut).Encode(&errorOutput{Error: message})
	} else {
		_, _ = fmt.Fprintln(r.errOut, message)
	}
	return exitCode
}

// openClient opens the cloudstic client from the given global flags.
// No-op if r.client is already set (e.g. injected for tests).
func (r *runner) openClient(ctx context.Context, g *globalFlags) error {
	if r.client != nil {
		return nil
	}
	cfg, err := resolveClientConfig(g)
	if err != nil {
		return err
	}
	client, err := openClient(ctx, cfg, nil)
	if err != nil {
		return err
	}
	r.client = client
	return nil
}

func defaultRunInteractiveCmd(ctx context.Context, stdin *os.File, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin == nil {
		stdin = os.Stdin
	}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
