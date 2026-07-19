package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/cloudstic/cli/internal/ui"
)

// environmentVariable is a documentation view derived from command flag
// specs. Only variables without a flag need an entry of their own.
type environmentVariable struct {
	Name        string
	Flag        string
	Description string
	Sensitive   bool
	Documented  bool
}

var standaloneEnvironmentVariables = []environmentVariable{
	{Name: "CLOUDSTIC_CONFIG_DIR", Description: "Cloudstic configuration directory", Documented: true},
}

func environmentVariables() []environmentVariable {
	var variables []environmentVariable
	seen := make(map[string]bool)
	appendFlag := func(spec flagSpec) {
		for _, environment := range spec.environment {
			if seen[environment.name] {
				continue
			}
			seen[environment.name] = true
			variables = append(variables, environmentVariable{
				Name:        environment.name,
				Flag:        spec.name,
				Description: spec.description,
				Sensitive:   environment.sensitive,
				Documented:  environment.documented,
			})
		}
	}
	for _, spec := range globalFlagSpecs {
		appendFlag(spec)
	}
	for _, command := range publicLeafCommands() {
		for _, spec := range command.flags {
			appendFlag(spec)
		}
	}
	for _, variable := range standaloneEnvironmentVariables {
		if !seen[variable.Name] {
			seen[variable.Name] = true
			variables = append(variables, variable)
		}
	}
	return variables
}

func environmentDocumentation() string {
	var out strings.Builder
	out.WriteString("Values resolve in this order: explicit CLI flag, environment variable, profile, built-in default.\n\n")
	out.WriteString("Provider-oriented variables remain deliberately unprefixed for compatibility with their existing ecosystems. Sensitive values are never rendered in help output.\n\n")
	out.WriteString("| Variable | Flag equivalent | Description |\n")
	out.WriteString("| :--- | :--- | :--- |\n")
	for _, variable := range environmentVariables() {
		if !variable.Documented {
			continue
		}
		flagName := "—"
		if variable.Flag != "" {
			flagName = "`-" + variable.Flag + "`"
		}
		description := variable.Description
		if variable.Sensitive {
			description += " (sensitive)"
		}
		_, _ = fmt.Fprintf(&out, "| `%s` | %s | %s |\n", variable.Name, flagName, description)
	}
	return out.String()
}

func printUsage(out io.Writer) {
	t := ui.NewTermWriter(out)

	_, _ = fmt.Fprintf(t.W, "%sCloudstic%s — Content-Addressable Backup System\n", ui.Bold, ui.Reset)

	t.Heading("USAGE")
	_, _ = fmt.Fprintf(t.W, "  cloudstic %s<command>%s [options]\n", ui.Cyan, ui.Reset)

	t.Heading("COMMANDS")
	commands := make([][2]string, 0, len(publicLeafCommands()))
	for _, command := range publicLeafCommands() {
		commands = append(commands, [2]string{command.path(), command.summary})
	}
	t.Commands(commands)

	general, encryption := splitGlobalFlagSpecs()
	t.HeadingSub("GLOBAL OPTIONS", "(also settable via environment variables)")
	t.Flags(usageFlags(general))
	t.Blank()
	t.Note(
		"Store URI formats:",
		"  local:<path>                       e.g. local:./backup_store",
		"  s3:<bucket>[/<prefix>]             e.g. s3:my-bucket or s3:my-bucket/prod",
		"  b2:<bucket>[/<prefix>]             e.g. b2:my-bucket or b2:my-bucket/prod",
		"  sftp://[user@]host[:port]/<path>   e.g. sftp://backup@host.com/backups",
	)

	t.Heading("ENCRYPTION OPTIONS")
	t.Flags(usageFlags(encryption))
	t.Blank()
	t.Note(
		"Encryption is required by default (AES-256-GCM). Provide -password",
		"or -encryption-key when running 'cloudstic init'. Use -recovery-key to open a",
		"repository with a recovery seed phrase.",
	)

	t.Heading("COMMAND DETAILS")
	for _, command := range publicLeafCommands() {
		t.Command(command.path(), command.args)
		if len(command.flags) != 0 {
			t.Flags(usageFlags(command.flags))
		}
		if len(command.notes) != 0 {
			t.Note(command.notes...)
		}
		t.Blank()
	}

	printEnvironmentUsage(t)

	t.Heading("EXAMPLES")
	t.Examples(
		`cloudstic init -password "my secret passphrase"`,
		`cloudstic init -password "my secret passphrase" -add-recovery-key`,
		"cloudstic backup -source local:./documents",
		"cloudstic backup -source gdrive -store b2:my-bucket",
		"cloudstic backup -source sftp://backup@host.com/data -source-sftp-key ~/.ssh/id_ed25519",
		"cloudstic list",
		"cloudstic restore",
		"cloudstic restore abc123 -output ./my-backup.zip",
		"cloudstic restore abc123 -format dir -output ./restored",
		"cloudstic restore abc123 -path Documents/report.pdf",
		"cloudstic restore abc123 -path Documents/",
		"cloudstic backup -source local:./documents -dry-run",
		"cloudstic prune -dry-run -verbose",
	)
	t.Blank()
}

func splitGlobalFlagSpecs() (general, encryption []flagSpec) {
	for _, spec := range globalFlagSpecs {
		if spec.section == flagSectionEncryption {
			encryption = append(encryption, spec)
		} else {
			general = append(general, spec)
		}
	}
	return general, encryption
}

func usageFlags(specs []flagSpec) [][2]string {
	flags := make([][2]string, 0, len(specs))
	for _, spec := range specs {
		label := "-" + spec.name
		if spec.takesValue() {
			label += " <" + spec.value + ">"
		}
		description := spec.description
		if names := documentedEnvironmentNames(spec); len(names) != 0 {
			description = ui.Env(description, strings.Join(names, ", "))
		}
		flags = append(flags, [2]string{label, description})
	}
	return flags
}

func documentedEnvironmentNames(spec flagSpec) []string {
	var names []string
	for _, environment := range spec.environment {
		if !environment.documented {
			continue
		}
		names = append(names, environment.name)
	}
	return names
}

func printEnvironmentUsage(t ui.TermWriter) {
	t.Heading("ENVIRONMENT")
	t.Note(
		"Values resolve in this order: explicit CLI flag, environment variable, profile, built-in default.",
		"Provider-standard variables remain unprefixed; sensitive values are never rendered in help output.",
	)
	var standalone [][2]string
	for _, variable := range standaloneEnvironmentVariables {
		if variable.Documented {
			standalone = append(standalone, [2]string{variable.Name, variable.Description})
		}
	}
	if len(standalone) != 0 {
		t.Blank()
		t.Flags(standalone)
	}
}
