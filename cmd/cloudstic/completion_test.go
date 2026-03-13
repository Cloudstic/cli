package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompletionBash(t *testing.T) {
	var buf bytes.Buffer
	completionBash(&buf)
	out := buf.String()

	if out == "" {
		t.Fatal("bash completion output is empty")
	}

	// Verify it's a valid bash completion script
	for _, marker := range []string{
		"_cloudstic()",
		"complete -F _cloudstic cloudstic",
		// All commands are listed
		"init", "backup", "restore", "list", "ls", "prune", "forget",
		"diff", "break-lock", "key", "cat", "completion",
		// Key subcommands
		"list add-recovery passwd",
		// Global flags
		"-store", "-password", "-verbose",
		// Command-specific flags
		"-dry-run", "-add-recovery-key", "-source", "-output",
		// Value completions
		"local: s3: b2: sftp://",
		"gdrive", "onedrive",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("bash completion missing expected marker: %q", marker)
		}
	}
}

func TestCompletionZsh(t *testing.T) {
	var buf bytes.Buffer
	completionZsh(&buf)
	out := buf.String()

	if out == "" {
		t.Fatal("zsh completion output is empty")
	}

	for _, marker := range []string{
		"#compdef cloudstic",
		"_cloudstic()",
		`_cloudstic "$@"`,
		// Commands with descriptions
		"init:Initialize a new repository",
		"backup:Create a new backup snapshot",
		"key:Manage encryption key slots",
		"completion:Generate shell completion scripts",
		// Key subcommands
		"list:List all encryption key slots",
		"add-recovery:Generate a 24-word recovery key",
		"passwd:Change the repository password",
		"-new-password[New repository password]",
		// Global flags with descriptions
		"-store[Storage backend URI",
		"-verbose[Log detailed operations]",
		// Subcommand-specific flags
		"-add-recovery-key[Generate a 24-word recovery key]",
		"-dry-run[Scan without writing]",
		"-keep-last[Keep N most recent snapshots]",
		// Value completions (source type list still present)
		"(local: sftp:// gdrive gdrive-changes onedrive onedrive-changes)",
		"(bash zsh fish)",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("zsh completion missing expected marker: %q", marker)
		}
	}
}

func TestCompletionFish(t *testing.T) {
	var buf bytes.Buffer
	completionFish(&buf)
	out := buf.String()

	if out == "" {
		t.Fatal("fish completion output is empty")
	}

	for _, marker := range []string{
		"complete -c cloudstic -f",
		// Subcommands
		"complete -c cloudstic -n __fish_use_subcommand -a init",
		"complete -c cloudstic -n __fish_use_subcommand -a backup",
		"complete -c cloudstic -n __fish_use_subcommand -a key",
		"complete -c cloudstic -n __fish_use_subcommand -a completion",
		// Key subcommands
		"-a list -d 'List all encryption key slots'",
		"-a add-recovery -d 'Generate a 24-word recovery key'",
		"-a passwd -d 'Change the repository password'",
		"-l new-password",
		// Global flags
		"complete -c cloudstic -l store -x",
		"complete -c cloudstic -l verbose",
		// Command-specific flags
		"__fish_seen_subcommand_from init",
		"__fish_seen_subcommand_from backup",
		"__fish_seen_subcommand_from forget",
		"-l dry-run",
		"-l keep-last",
		"-l add-recovery-key",
		// Value completions (source type list still present)
		"'local: sftp:// gdrive gdrive-changes onedrive onedrive-changes'",
		"'bash zsh fish'",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("fish completion missing expected marker: %q", marker)
		}
	}
}
