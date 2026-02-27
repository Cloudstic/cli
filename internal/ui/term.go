package ui

import (
	"fmt"
	"io"
)

const (
	Bold  = "\033[1m"
	Dim   = "\033[2m"
	Cyan  = "\033[36m"
	Green = "\033[32m"
	Reset = "\033[0m"
)

// TermWriter provides helpers for styled terminal output.
type TermWriter struct{ W io.Writer }

// NewTermWriter returns a TermWriter targeting the given writer.
func NewTermWriter(w io.Writer) TermWriter { return TermWriter{W: w} }

func (t TermWriter) Heading(title string) {
	fmt.Fprintf(t.W, "\n%s%s%s\n", Bold, title, Reset)
}

func (t TermWriter) HeadingSub(title, subtitle string) {
	fmt.Fprintf(t.W, "\n%s%s%s%s %s%s\n", Bold, title, Reset, Dim, subtitle, Reset)
}

func (t TermWriter) Command(name, args string) {
	if args != "" {
		fmt.Fprintf(t.W, "  %s%s%s %s%s%s\n", Bold, name, Reset, Dim, args, Reset)
	} else {
		fmt.Fprintf(t.W, "  %s%s%s\n", Bold, name, Reset)
	}
}

func (t TermWriter) Commands(cmds [][2]string) {
	for _, c := range cmds {
		fmt.Fprintf(t.W, "  %s%-18s%s %s\n", Green, c[0], Reset, c[1])
	}
}

func (t TermWriter) Flags(flags [][2]string) {
	for _, f := range flags {
		fmt.Fprintf(t.W, "    %s%-22s%s %s\n", Cyan, f[0], Reset, f[1])
	}
}

func (t TermWriter) Note(lines ...string) {
	for _, l := range lines {
		fmt.Fprintf(t.W, "  %s%s%s\n", Dim, l, Reset)
	}
}

func (t TermWriter) Examples(cmds ...string) {
	for _, c := range cmds {
		fmt.Fprintf(t.W, "  %s$%s %s\n", Dim, Reset, c)
	}
}

func (t TermWriter) Blank() { fmt.Fprintln(t.W) }

// Env wraps a description with a dimmed env-var hint.
func Env(desc, envVar string) string {
	return desc + "  " + Dim + "[env: " + envVar + "]" + Reset
}
