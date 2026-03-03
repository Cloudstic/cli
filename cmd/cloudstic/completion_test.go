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
		"diff", "break-lock", "add-recovery-key", "cat", "completion",
		// Global flags
		"-store", "-encryption-password", "-verbose",
		// Command-specific flags
		"-dry-run", "-recovery", "-source", "-output",
		// Value completions
		"local b2 s3 sftp",
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
		"completion:Generate shell completion scripts",
		// Global flags with descriptions
		"-store[Storage backend]",
		"-verbose[Log detailed operations]",
		// Subcommand-specific flags
		"-recovery[Generate a 24-word recovery key]",
		"-dry-run[Scan without writing]",
		"-keep-last[Keep N most recent snapshots]",
		// Value completions
		"(local b2 s3 sftp)",
		"(local sftp gdrive gdrive-changes onedrive onedrive-changes)",
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
		"complete -c cloudstic -n __fish_use_subcommand -a completion",
		// Global flags
		"complete -c cloudstic -l store -x",
		"complete -c cloudstic -l verbose",
		// Command-specific flags
		"__fish_seen_subcommand_from init",
		"__fish_seen_subcommand_from backup",
		"__fish_seen_subcommand_from forget",
		"-l dry-run",
		"-l keep-last",
		"-l recovery",
		// Value completions
		"'local b2 s3 sftp'",
		"'local sftp gdrive gdrive-changes onedrive onedrive-changes'",
		"'bash zsh fish'",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("fish completion missing expected marker: %q", marker)
		}
	}
}

func TestCompletionE2E(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	bin := buildBinary(t)

	// Test each shell
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			out := run(t, bin, "completion", shell)
			if out == "" {
				t.Fatalf("completion %s produced empty output", shell)
			}
			// Verify it contains the command name at minimum
			if !strings.Contains(out, "cloudstic") {
				t.Errorf("completion %s output missing 'cloudstic'", shell)
			}
		})
	}

	// Test unsupported shell
	out := runExpectFail(t, bin, "completion", "powershell")
	if !strings.Contains(out, "Unsupported shell") {
		t.Errorf("Expected unsupported shell error, got: %s", out)
	}

	// Test no shell argument
	out = runExpectFail(t, bin, "completion")
	if !strings.Contains(out, "Usage:") {
		t.Errorf("Expected usage message, got: %s", out)
	}
}
