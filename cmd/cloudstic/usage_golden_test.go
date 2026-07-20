package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes styling so golden files stay readable and reviewable.
func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }

// TestRootUsageGolden pins the exact text of `cloudstic help`. Help output is
// assembled from the command tree and the global flag specifications, so this
// is the check that catches drift in either — including flags appearing in the
// wrong section, losing their env annotation, or falling out of alignment.
func TestRootUsageGolden(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	var buf bytes.Buffer
	printUsage(&buf)
	assertGolden(t, "usage_root", stripANSI(buf.String()))
}

// TestCommandHelpGolden pins per-command help for a leaf with positional
// arguments, a leaf with none, and a group.
func TestCommandHelpGolden(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	for _, tc := range []struct{ golden, path string }{
		{"help_check", "check"},
		{"help_list", "list"},
		{"help_key", "key"},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			c, ok := lookupCommand(tc.path)
			if !ok {
				t.Fatalf("command %q not in registry", tc.path)
			}
			var buf bytes.Buffer
			printCommandHelp(&buf, c, tc.path)
			assertGolden(t, tc.golden, stripANSI(buf.String()))
		})
	}
}

// TestCommandHelpShowsEveryDeclaredFlag guarantees the trade made when root
// help stopped listing per-command flags: every flag a command declares must
// be reachable from that command's own -h output. Without this, dropping the
// COMMAND DETAILS section could hide a flag entirely.
func TestCommandHelpShowsEveryDeclaredFlag(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	walk(commandRegistry(), func(path string, c command) {
		if c.flags == nil || c.hidden {
			return
		}
		var buf bytes.Buffer
		printCommandHelp(&buf, c, path)
		help := stripANSI(buf.String())

		cf := c.flags()
		for _, spec := range cf.specs() {
			if !strings.Contains(help, "-"+spec.name) {
				t.Errorf("`cloudstic %s -h` does not document -%s", path, spec.name)
			}
		}
		for _, positional := range c.positionals {
			if !strings.Contains(help, positionalSynopsis(positional)) {
				t.Errorf("`cloudstic %s -h` does not document positional %q", path, positional.name)
			}
		}
	})
}
