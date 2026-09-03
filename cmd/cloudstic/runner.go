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

	cloudstic "github.com/cloudstic/cli"
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
		if !ok {
			continue
		}
		if errors.Is(err, context.Canceled) {
			message, exitCode = "Interrupted.", exitInterrupted
			break
		}
		if errors.Is(err, cloudstic.ErrRepoLocked) {
			message += " Run `cloudstic break-lock` to remove a stale lock left by a crashed process."
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

// openClient connects to the repository described by cfg.
// No-op if r.client is already set (e.g. injected for tests).
//
// It takes a resolved configuration rather than the flags it came from. That
// keeps this type to the I/O primitives it is meant to hold: resolving flags
// against a profile means reading the profiles file, and hiding a file read
// behind "open the client" put it out of sight of every call site. Each command
// now resolves first — the same two steps `init` and the `key` subcommands
// always used.
func (r *runner) openClient(ctx context.Context, cfg clientConfig) error {
	if r.client != nil {
		return nil
	}
	client, err := openClient(ctx, cfg, nil)
	if err != nil {
		return err
	}
	r.client = client
	printLegacyFormatNote(r.errOut, cfg, client.RepoFormat())
	return nil
}

// printLegacyFormatNote tells the user, once per command, that this repository
// is still on the packfile format and what to do about it.
//
// Here rather than in the library: a repository staying on format 2 is not an
// error and the client has nothing to say about it, but a person running the
// CLI is the one who can act. It goes to stderr so it never contaminates
// stdout that another program is parsing, and -quiet silences it for anyone
// who has decided to stay on format 2 — which is a supported choice, since a
// packfile repository is what a build older than v3 support can read.
// A free function taking the writer first, like every other print* helper:
// deciding what to say about a format is presentation, not one of the I/O
// primitives the runner is allowed to hold (TestRunnerMethodsAreIOPrimitivesOnly).
func printLegacyFormatNote(out io.Writer, cfg clientConfig, format int) {
	if cfg.Quiet || format == 0 || format >= cloudstic.RepoFormatV3 {
		return
	}
	_, _ = fmt.Fprintf(out,
		"Note: this repository uses format %d (packfiles). Format %d is faster to read, "+
			"uses less memory, and is smaller under a retention policy.\n"+
			"      Migrate with: cloudstic migrate -store %s -to <new-store>\n",
		format, cloudstic.RepoFormatV3, cfg.Store.URI)
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
