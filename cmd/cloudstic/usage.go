package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/cloudstic/cli/internal/ui"
)

// helpSection groups related global flags under one heading, with optional
// prose. Which flags exist is declared in flags.go; how they are presented is
// declared here. TestHelpSectionsCoverEveryGlobalFlag guarantees the two
// cannot drift apart.
type helpSection struct {
	title    string
	subtitle string
	groups   []flagGroup
	notes    []string
}

// globalHelpSections is the presentation order and grouping of global flags.
func globalHelpSections() []helpSection {
	return []helpSection{
		{
			title:    "GLOBAL OPTIONS",
			subtitle: "(also settable via environment variables)",
			groups:   []flagGroup{repoFlagSpecs, storeSFTPFlagSpecs, sourceSFTPFlagSpecs, outputFlagSpecs, baseFlagSpecs},
			notes: []string{
				"Store URI formats:",
				"  local:<path>                       e.g. local:./backup_store",
				"  s3:<bucket>[/<prefix>]             e.g. s3:my-bucket or s3:my-bucket/prod",
				"  b2:<bucket>[/<prefix>]             e.g. b2:my-bucket or b2:my-bucket/prod",
				"  sftp://[user@]host[:port]/<path>   e.g. sftp://backup@host.com/backups",
			},
		},
		{
			title:  "ENCRYPTION OPTIONS",
			groups: []flagGroup{encryptionFlagSpecs},
			notes: []string{
				"Encryption is required by default (AES-256-GCM). Provide -password",
				"or -encryption-key when running 'cloudstic init'. Use -recovery-key to",
				"open a repository with a recovery seed phrase.",
			},
		},
	}
}

// sectionSpecs returns the flag specifications a section presents.
func (s helpSection) sectionSpecs() []flagSpec {
	g := &globalFlags{}
	var specs []flagSpec
	for _, group := range s.groups {
		specs = append(specs, group(g)...)
	}
	return specs
}

// printUsage renders root help entirely from the command registry and global
// flag specifications.
func printUsage(out io.Writer) {
	t := ui.NewTermWriter(out)
	printHelpTitle(t)

	t.Heading("USAGE")
	_, _ = fmt.Fprintf(t.W, "  cloudstic %s<command>%s [options]\n", ui.Cyan, ui.Reset)

	t.Heading("COMMANDS")
	t.Commands(usageCommandRows())

	sections := globalHelpSections()
	width := 0
	for _, section := range sections {
		width = max(width, flagColumnWidth(section.sectionSpecs()))
	}
	for _, section := range sections {
		if section.subtitle != "" {
			t.HeadingSub(section.title, section.subtitle)
		} else {
			t.Heading(section.title)
		}
		t.Flags(padFlagRows(flagRows(section.sectionSpecs()), width))
		if len(section.notes) > 0 {
			t.Blank()
			t.Note(section.notes...)
		}
	}

	t.Heading("EXAMPLES")
	t.Examples(
		`cloudstic init -password "my secret passphrase"`,
		"cloudstic backup -source local:./documents",
		"cloudstic backup -source gdrive -store b2:my-bucket",
		"cloudstic list",
		"cloudstic restore abc123 -output ./my-backup.zip",
		"cloudstic restore abc123 -format dir -output ./restored",
		"cloudstic forget --keep-last 10 --prune",
		"cloudstic check -read-data",
	)

	t.Blank()
	t.Note("Run 'cloudstic <command> -h' for command-specific help.")
	t.Blank()
}

// flagColumnWidth returns the widest flag synopsis in specs.
func flagColumnWidth(specs []flagSpec) int {
	width := 0
	for _, spec := range specs {
		width = max(width, len(flagSynopsis(spec)))
	}
	return width
}

// padFlagRows pads each synopsis to width so that separately rendered sections
// share one column alignment.
func padFlagRows(rows [][2]string, width int) [][2]string {
	for i, row := range rows {
		if pad := width - len(row[0]); pad > 0 {
			rows[i][0] = row[0] + strings.Repeat(" ", pad)
		}
	}
	return rows
}

// printCommandHelp renders help for a group or leaf from its command metadata.
func printCommandHelp(out io.Writer, c command, path string) {
	t := ui.NewTermWriter(out)
	printHelpTitle(t)

	t.Heading("COMMAND")
	t.Command("cloudstic "+path, commandArguments(c))
	t.Note(c.summary)

	if c.isGroup() {
		t.Heading("SUBCOMMANDS")
		t.Commands(commandRows(c.visibleChildren()))
		t.Blank()
		return
	}

	cf := c.flags()

	// One column width across every section of this command's help, so the
	// ARGUMENTS, OPTIONS and GLOBAL OPTIONS blocks line up with each other.
	width := flagColumnWidth(cf.ownSpecs())
	width = max(width, flagColumnWidth(cf.global))
	for _, positional := range c.positionals {
		width = max(width, len(positionalSynopsis(positional)))
	}

	if len(c.positionals) > 0 {
		t.Heading("ARGUMENTS")
		t.Flags(padFlagRows(positionalRows(c.positionals), width))
	}

	if len(cf.ownSpecs()) > 0 {
		t.Heading("OPTIONS")
		t.Flags(padFlagRows(flagRows(cf.ownSpecs()), width))
	}
	if len(cf.global) > 0 {
		t.HeadingSub("GLOBAL OPTIONS", "(also settable via environment variables)")
		t.Flags(padFlagRows(flagRows(cf.global), width))
	}
	t.Blank()
}

// printCommandSynopsis renders the compact usage line used before parse and
// semantic-validation errors.
func printCommandSynopsis(out io.Writer, c command, path string) {
	_, _ = fmt.Fprintf(out, "Usage: cloudstic %s", path)
	if args := commandArguments(c); args != "" {
		_, _ = fmt.Fprintf(out, " %s", args)
	}
	_, _ = fmt.Fprintln(out)
}

func printHelpTitle(t ui.TermWriter) {
	_, _ = fmt.Fprintf(t.W, "%sCloudstic%s — Content-Addressable Backup System\n", ui.Bold, ui.Reset)
}

func commandArguments(c command) string {
	if c.isGroup() {
		return "<subcommand> [options]"
	}
	parts := []string{"[options]"}
	for _, positional := range c.positionals {
		parts = append(parts, positionalSynopsis(positional))
	}
	return strings.Join(parts, " ")
}

func positionalSynopsis(spec positionalSpec) string {
	name := strings.ToLower(strings.ReplaceAll(spec.name, " ", "_"))
	switch {
	case spec.required && spec.repeatable:
		return "<" + name + ">..."
	case spec.required:
		return "<" + name + ">"
	case spec.repeatable:
		return "[" + name + "...]"
	default:
		return "[" + name + "]"
	}
}

func positionalRows(specs []positionalSpec) [][2]string {
	rows := make([][2]string, 0, len(specs))
	for _, spec := range specs {
		description := "Optional."
		if spec.required {
			description = "Required."
		}
		if spec.repeatable {
			description = strings.TrimSuffix(description, ".") + "; may be repeated."
		}
		if spec.defaultValue != "" {
			description = strings.TrimSuffix(description, ".") + "; default: " + spec.defaultValue + "."
		}
		rows = append(rows, [2]string{positionalSynopsis(spec), description})
	}
	return rows
}

func flagRows(specs []flagSpec) [][2]string {
	rows := make([][2]string, 0, len(specs))
	for _, spec := range specs {
		description := spec.usage
		if spec.env != "" {
			description = ui.Env(description, spec.env)
		}
		rows = append(rows, [2]string{flagSynopsis(spec), description})
	}
	return rows
}

func flagSynopsis(spec flagSpec) string {
	if spec.isBool {
		return "-" + spec.name
	}
	placeholder := spec.placeholder
	if placeholder == "" {
		placeholder = "<value>"
	}
	return "-" + spec.name + " " + placeholder
}

func commandRows(commands []command) [][2]string {
	rows := make([][2]string, 0, len(commands))
	for _, c := range commands {
		rows = append(rows, [2]string{c.name, c.summary})
	}
	return rows
}
